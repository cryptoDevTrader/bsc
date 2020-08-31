package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/accounts/usbwallet"
	"github.com/ethereum/go-ethereum/cmd/token-bind-tool/bep20"
	"github.com/ethereum/go-ethereum/cmd/token-bind-tool/ownable"
	tokenmanager "github.com/ethereum/go-ethereum/cmd/token-bind-tool/tokenmanger"
	"github.com/ethereum/go-ethereum/cmd/token-bind-tool/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

var (
	tokenManager = common.HexToAddress("0x0000000000000000000000000000000000001008")
)

type Config struct {
	ContractData  string `json:"contract_data"`
	Symbol        string `json:"symbol"`
	BEP2Symbol    string `json:"bep2_symbol"`
	LedgerAccount string `json:"ledger_account"`
}

func printUsage() {
	fmt.Print("usage: ./token-bind-tool --network-type testnet --operation {initKey, deployContract, approveBindAndTransferOwnership or refundRestBNB}\n")
}

func initFlags() {
	flag.String(utils.NetworkType, utils.TestNet, "mainnet or testnet")
	flag.String(utils.ConfigPath, "", "config file path")
	flag.String(utils.Operation, "", "operation to perform")
	flag.String(utils.BEP20ContractAddr, "", "bep20 contract address")
	flag.String(utils.LedgerAccount, "", "ledger account address")
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	err := viper.BindPFlags(pflag.CommandLine)
	if err != nil {
		panic(err)
	}
}

func readConfigData(configPath string) (Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return Config{}, err
	}
	fileData, err := ioutil.ReadAll(file)
	if err != nil {
		return Config{}, err
	}
	var config Config
	err = json.Unmarshal(fileData, &config)
	if err != nil {
		return Config{}, err
	}
	return config, nil
}

func generateOrGetTempAccount() (*keystore.KeyStore, accounts.Account, error) {
	path, err := os.Getwd()
	if err != nil {
		return nil, accounts.Account{}, err
	}
	if _, err := os.Stat(filepath.Join(path, utils.BindKeystore)); os.IsNotExist(err) {
		err = os.Mkdir(filepath.Join(path, utils.BindKeystore), os.ModePerm)
		if err != nil {
			panic(err)
		}
	}
	keyStore := keystore.NewKeyStore(filepath.Join(path, utils.BindKeystore), keystore.StandardScryptN, keystore.StandardScryptP)
	var files []string
	err = filepath.Walk(filepath.Join(path, utils.BindKeystore), func(path string, info os.FileInfo, err error) error {
		files = append(files, path)
		return nil
	})
	files = files[1:]
	if len(files) == 0 {
		newAccount, err := keyStore.NewAccount(utils.Passwd)
		if err != nil {
			return nil, accounts.Account{}, err
		}
		err = keyStore.Unlock(newAccount, utils.Passwd)
		if err != nil {
			return nil, accounts.Account{}, err
		}
		fmt.Println(fmt.Sprintf("Create new account %s", newAccount.Address.String()))
		return keyStore, newAccount, nil
	} else if len(files) == 1 {
		accountList := keyStore.Accounts()
		if len(accountList) != 1 {
			return nil, accounts.Account{}, err
		}
		account := accountList[0]
		fmt.Println(fmt.Sprintf("Load account %s", account.Address.String()))
		err = keyStore.Unlock(account, utils.Passwd)
		if err != nil {
			return nil, accounts.Account{}, err
		}
		return keyStore, account, nil
	} else {
		return nil, accounts.Account{}, fmt.Errorf("expect only one or zero keystore file in %s", filepath.Join(path, utils.BindKeystore))
	}
}

func openLedger(ethClient *ethclient.Client) (accounts.Wallet, accounts.Account, error) {
	ledgerHub, err := usbwallet.NewLedgerHub()
	if err != nil {
		return nil, accounts.Account{}, fmt.Errorf("failed to start Ledger hub, disabling: %v", err)
	}
	wallets := ledgerHub.Wallets()
	if len(wallets) == 0 {
		return nil, accounts.Account{}, fmt.Errorf("empty ledger wallet")
	}
	wallet := wallets[0]
	err = wallet.Close()
	if err != nil {
		fmt.Println(err.Error())
	}

	err = wallet.Open("")
	if err != nil {
		return nil, accounts.Account{}, fmt.Errorf("failed to start Ledger hub, disabling: %v", err)
	}

	walletStatus, err := wallet.Status()
	if err != nil {
		return nil, accounts.Account{}, fmt.Errorf("failed to start Ledger hub, disabling: %v", err)
	}
	fmt.Println(walletStatus)
	//fmt.Println(wallet.URL())

	wallet.SelfDerive([]accounts.DerivationPath{accounts.LegacyLedgerBaseDerivationPath, accounts.DefaultBaseDerivationPath}, ethClient)
	utils.Sleep(3)
	if len(wallet.Accounts()) == 0 {
		return nil, accounts.Account{}, fmt.Errorf("empty ledger account")
	}
	ledgerAccount := wallet.Accounts()[0]

	fmt.Println(fmt.Sprintf("Ledger account %s", ledgerAccount.Address.String()))

	return wallet, ledgerAccount, nil
}

func main() {
	initFlags()

	networkType := viper.GetString(utils.NetworkType)
	configPath := viper.GetString(utils.ConfigPath)
	operation := viper.GetString(utils.Operation)
	if operation != utils.DeployContract && operation != utils.ApproveBind && operation != utils.InitKey && operation != utils.RefundRestBNB ||
		networkType != utils.TestNet && networkType != utils.Mainnet {
		printUsage()
		return
	}
	var rpcClient *rpc.Client
	var err error
	var chainId *big.Int
	if networkType == utils.Mainnet {
		rpcClient, err = rpc.DialContext(context.Background(), utils.MainnnetRPC)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		chainId = big.NewInt(utils.MainnetChainID)
	} else {
		rpcClient, err = rpc.DialContext(context.Background(), utils.TestnetRPC)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		chainId = big.NewInt(utils.TestnetChainID)
	}
	ethClient := ethclient.NewClient(rpcClient)

	if operation == utils.InitKey {
		_, tempAccount, err := generateOrGetTempAccount()
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		ledgerWallet, ledgerAccount, err := openLedger(ethClient)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		defer ledgerWallet.Close()
		fmt.Println(fmt.Sprintf("Ledger account %s, Temp account: %s", ledgerAccount.Address.String(), tempAccount.Address.String()))
		return
	}

	keyStore, tempAccount, err := generateOrGetTempAccount()
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	if operation == utils.DeployContract {
		configData, err := readConfigData(configPath)
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		contractAddr, err := TransferBNBAndDeployContractFromKeystoreAccount(ethClient, keyStore, tempAccount, configData, chainId)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		fmt.Println(fmt.Sprintf("For BEP2 token %s, the deployed BEP20 configData address is %s", configData.BEP2Symbol, contractAddr.String()))
	} else if operation == utils.ApproveBind {
		configData, err := readConfigData(configPath)
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		bep20ContractAddr := viper.GetString(utils.BEP20ContractAddr)
		if bep20ContractAddr == "" {
			fmt.Println("bep20 configData address is empty")
			return
		}
		ApproveBindAndTransferOwnershipAndRestBalanceBackToLedgerAccount(ethClient, keyStore, tempAccount, configData, common.HexToAddress(bep20ContractAddr), chainId)
	} else {
		ledgerAccount := common.HexToAddress(viper.GetString(utils.LedgerAccount))
		RefundRestBNB(ethClient, keyStore, tempAccount, ledgerAccount, chainId)
	}

}

func TransferBNBAndDeployContractFromKeystoreAccount(ethClient *ethclient.Client, keyStore *keystore.KeyStore, tempAccount accounts.Account, contract Config, chainId *big.Int) (common.Address, error) {
	fmt.Println(fmt.Sprintf("Deploy BEP20 contract %s from account %s", contract.Symbol, tempAccount.Address.String()))
	contractByteCode, err := hex.DecodeString(contract.ContractData)
	if err != nil {
		return common.Address{}, err
	}
	txHash, err := utils.DeployBEP20Contract(ethClient, keyStore, tempAccount, contractByteCode, chainId)
	if err != nil {
		return common.Address{}, err
	}
	utils.Sleep(10)

	txRecipient, err := ethClient.TransactionReceipt(context.Background(), txHash)
	if err != nil {
		return common.Address{}, err
	}
	contractAddr := txRecipient.ContractAddress
	fmt.Println(fmt.Sprintf("BEP20 contract addrss: %s", contractAddr.String()))
	return contractAddr, nil
}

func ApproveBindAndTransferOwnershipAndRestBalanceBackToLedgerAccount(ethClient *ethclient.Client, keyStore *keystore.KeyStore, tempAccount accounts.Account, configData Config, bep20ContractAddr common.Address, chainId *big.Int) {
	bep20Instance, err := bep20.NewBep20(bep20ContractAddr, ethClient)
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	totalSupply, err := bep20Instance.TotalSupply(utils.GetCallOpts())
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println(fmt.Sprintf("Total Supply %s", totalSupply.String()))

	fmt.Println(fmt.Sprintf("Approve %s:%s to TokenManager from %s", totalSupply.String(), configData.Symbol, tempAccount.Address.String()))
	approveTxHash, err := bep20Instance.Approve(utils.GetTransactor(ethClient, keyStore, tempAccount, big.NewInt(0)), tokenManager, totalSupply)
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println(fmt.Sprintf("Approve token to tokenManager txHash %s", approveTxHash.Hash().String()))

	utils.Sleep(20)

	tokenManagerInstance, _ := tokenmanager.NewTokenmanager(tokenManager, ethClient)
	approveBindTx, err := tokenManagerInstance.ApproveBind(utils.GetTransactor(ethClient, keyStore, tempAccount, big.NewInt(1e16)), bep20ContractAddr, configData.BEP2Symbol)
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println(fmt.Sprintf("ApproveBind txHash %s", approveBindTx.Hash().String()))

	utils.Sleep(10)

	approveBindTxRecipient, err := ethClient.TransactionReceipt(context.Background(), approveBindTx.Hash())
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println("Track approveBind Tx status")
	if approveBindTxRecipient.Status != 1 {
		fmt.Println("Approve Bind Failed")
		rejectBindTx, err := tokenManagerInstance.RejectBind(utils.GetTransactor(ethClient, keyStore, tempAccount, big.NewInt(1e16)), bep20ContractAddr, configData.BEP2Symbol)
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		fmt.Println(fmt.Sprintf("rejectBind txHash %s", rejectBindTx.Hash().String()))
		utils.Sleep(10)
		fmt.Println("Track rejectBind Tx status")
		rejectBindTxRecipient, err := ethClient.TransactionReceipt(context.Background(), rejectBindTx.Hash())
		if err != nil {
			fmt.Println(err.Error())
			return
		}
		fmt.Println(fmt.Sprintf("reject bind tx recipient status %d", rejectBindTxRecipient.Status))
		return
	}

	utils.Sleep(10)
	ownershipInstance, err := ownable.NewOwnable(bep20ContractAddr, ethClient)
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println(fmt.Sprintf("Transfer ownership %s %s to ledger account %s", totalSupply.String(), configData.Symbol, tempAccount.Address.String()))
	transferOwnerShipTxHash, err := ownershipInstance.TransferOwnership(utils.GetTransactor(ethClient, keyStore, tempAccount, big.NewInt(0)), common.HexToAddress(configData.LedgerAccount))
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println(fmt.Sprintf("transfer ownership txHash %s", transferOwnerShipTxHash.Hash().String()))
}

func RefundRestBNB(ethClient *ethclient.Client, keyStore *keystore.KeyStore, tempAccount accounts.Account, ledgerAccount common.Address, chainId *big.Int) {
	err := utils.SendBNBBackToLegerAccount(ethClient, keyStore, tempAccount, ledgerAccount, chainId)
	if err != nil {
		fmt.Println(err.Error())
	}
}