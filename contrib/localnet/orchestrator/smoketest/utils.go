package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zeta-chain/zetacore/common"
	fungibletypes "github.com/zeta-chain/zetacore/x/fungible/types"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil"
	"github.com/ethereum/go-ethereum"
	ethcommon "github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
	coretypes "github.com/tendermint/tendermint/rpc/core/types"
	crosschaintypes "github.com/zeta-chain/zetacore/x/crosschain/types"
)

// WaitCctxMinedByInTxHash waits until cctx is mined; returns the cctxIndex (the last one)
func WaitCctxMinedByInTxHash(inTxHash string, cctxClient crosschaintypes.QueryClient) *crosschaintypes.CrossChainTx {
	cctxs := WaitCctxsMinedByInTxHash(inTxHash, cctxClient)
	return cctxs[len(cctxs)-1]
}

// WaitCctxsMinedByInTxHash waits until cctx is mined; returns the cctxIndex (the last one)
func WaitCctxsMinedByInTxHash(inTxHash string, cctxClient crosschaintypes.QueryClient) []*crosschaintypes.CrossChainTx {
	var cctxIndexes []string
	for {
		time.Sleep(5 * time.Second)
		fmt.Printf("Waiting for cctx to be mined by inTxHash: %s\n", inTxHash)
		res, err := cctxClient.InTxHashToCctx(context.Background(), &crosschaintypes.QueryGetInTxHashToCctxRequest{InTxHash: inTxHash})
		if err != nil {
			fmt.Println("Error getting cctx by inTxHash: ", err.Error())
			continue
		}
		cctxIndexes = res.InTxHashToCctx.CctxIndex
		fmt.Printf("Deposit receipt cctx index: %v\n", cctxIndexes)
		break
	}
	var wg sync.WaitGroup
	var cctxs []*crosschaintypes.CrossChainTx
	for _, cctxIndex := range cctxIndexes {
		cctxIndex := cctxIndex
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				time.Sleep(3 * time.Second)
				res, err := cctxClient.Cctx(context.Background(), &crosschaintypes.QueryGetCctxRequest{Index: cctxIndex})
				if err == nil && IsTerminalStatus(res.CrossChainTx.CctxStatus.Status) {
					fmt.Printf("Deposit receipt cctx status: %+v; The cctx is processed\n", res.CrossChainTx.CctxStatus.Status.String())
					cctxs = append(cctxs, res.CrossChainTx)
					break
				} else if err != nil {
					fmt.Println("Error getting cctx by index: ", err.Error())
				} else {
					cctxStatus := res.CrossChainTx.CctxStatus
					fmt.Printf(
						"Deposit receipt cctx status: %s; Message: %s; Waiting for the cctx to be processed\n",
						cctxStatus.Status.String(),
						cctxStatus.StatusMessage,
					)
				}
			}
		}()
	}
	wg.Wait()
	return cctxs
}

func IsTerminalStatus(status crosschaintypes.CctxStatus) bool {
	return status == crosschaintypes.CctxStatus_OutboundMined || status == crosschaintypes.CctxStatus_Aborted || status == crosschaintypes.CctxStatus_Reverted
}

func LoudPrintf(format string, a ...any) {
	fmt.Println("=======================================")
	fmt.Printf(format, a...)
	fmt.Println("=======================================")
}

func CheckNonce(client *ethclient.Client, addr ethcommon.Address, expectedNonce uint64) error {
	nonce, err := client.PendingNonceAt(context.Background(), addr)
	if err != nil {
		return err
	}
	if nonce != expectedNonce {
		return fmt.Errorf("want nonce %d; got %d", expectedNonce, nonce)
	}
	return nil
}

// MustWaitForTxReceipt waits until a broadcasted tx to be mined and return its receipt
// timeout and panic after 30s.
func MustWaitForTxReceipt(client *ethclient.Client, tx *ethtypes.Transaction) *ethtypes.Receipt {
	start := time.Now()
	for {
		if time.Since(start) > 30*time.Second {
			panic("waiting tx receipt timeout")
		}
		time.Sleep(1 * time.Second)
		receipt, err := client.TransactionReceipt(context.Background(), tx.Hash())
		if err != nil {
			if !errors.Is(err, ethereum.NotFound) {
				fmt.Println("fetching tx receipt error: ", err.Error())
			}
			continue
		}
		if receipt != nil {
			return receipt
		}
	}
}

// ScriptPKToAddress is a hex string for P2WPKH script
func ScriptPKToAddress(scriptPKHex string) string {
	pkh, err := hex.DecodeString(scriptPKHex[4:])
	if err == nil {
		addr, err := btcutil.NewAddressWitnessPubKeyHash(pkh, &chaincfg.RegressionNetParams)
		if err == nil {
			return addr.EncodeAddress()
		}
	}
	return ""
}

// WaitForBlockHeight waits until the block height reaches the given height
func WaitForBlockHeight(height int64) {
	// initialize rpc and check status
	rpc, err := rpchttp.New("http://zetacore0:26657", "/websocket")
	if err != nil {
		panic(err)
	}
	status := &coretypes.ResultStatus{}
	for status.SyncInfo.LatestBlockHeight < height {
		status, err = rpc.Status(context.Background())
		if err != nil {
			panic(err)
		}
		time.Sleep(time.Second * 5)
		fmt.Printf("waiting for block: %d, current height: %d\n", height, status.SyncInfo.LatestBlockHeight)
	}
}

// DeploySystemContractsAndZRC20 deploys the system contracts and ZRC20 contracts
func DeploySystemContractsAndZRC20(zetaTxServer ZetaTxServer) error {
	// Deploy new system contracts
	res, err := zetaTxServer.BroadcastTx(FungibleAdminName, fungibletypes.NewMsgDeploySystemContracts(FungibleAdminAddress))
	if err != nil {
		return err
	}
	fmt.Println("System contracts deployed")

	address, err := fetchAttribute(res, "SystemContractAddress")
	if err != nil {
		return err
	}

	// set system contract
	_, err = zetaTxServer.BroadcastTx(FungibleAdminName, fungibletypes.NewMsgUpdateSystemContract(FungibleAdminAddress, address))
	if err != nil {
		return err
	}

	// set uniswap contract addresses
	UniswapV2FactoryAddr, err = fetchAttribute(res, "UniswapV2Factory")
	if err != nil {
		return err
	}
	UniswapV2RouterAddr, err = fetchAttribute(res, "DeployUniswapV2Router02")
	if err != nil {
		return err
	}

	// deploy eth zrc20
	_, err = zetaTxServer.BroadcastTx(FungibleAdminName, fungibletypes.NewMsgDeployFungibleCoinZRC20(
		FungibleAdminAddress,
		"",
		common.GoerliLocalnetChain().ChainId,
		18,
		"ETH",
		"gETH",
		common.CoinType_Gas,
		1000000,
	))
	if err != nil {
		return err
	}

	// deploy btc zrc20
	_, err = zetaTxServer.BroadcastTx(FungibleAdminName, fungibletypes.NewMsgDeployFungibleCoinZRC20(
		FungibleAdminAddress,
		"",
		common.BtcRegtestChain().ChainId,
		8,
		"BTC",
		"tBTC",
		common.CoinType_Gas,
		1000000,
	))
	if err != nil {
		return err
	}

	// deploy usdt zrc20
	res, err = zetaTxServer.BroadcastTx(FungibleAdminName, fungibletypes.NewMsgDeployFungibleCoinZRC20(
		FungibleAdminAddress,
		USDTERC20Addr,
		common.GoerliLocalnetChain().ChainId,
		6,
		"USDT",
		"USDT",
		common.CoinType_ERC20,
		1000000,
	))
	if err != nil {
		return err
	}

	// fetch the usdt zrc20 contract address and remove the quotes
	address, err = fetchAttribute(res, "Contract")
	if err != nil {
		return err
	}
	if len(address) < 2 {
		return fmt.Errorf("invalid address in event: %s", address)
	}
	address = address[1 : len(address)-1]
	if !ethcommon.IsHexAddress(address) {
		return fmt.Errorf("invalid address in event: %s", address)
	}
	USDTZRC20Addr = address
	return nil
}
