package web3

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"

	"github.com/gochain-io/gochain/v3/common"
	"github.com/gochain-io/gochain/v3/common/hexutil"
	"github.com/gochain-io/gochain/v3/core/types"
	"github.com/gochain-io/gochain/v3/rlp"
	"github.com/gochain-io/gochain/v3/rpc"
)

type Client interface {
	GetBalance(ctx context.Context, address string, blockNumber *big.Int) (*big.Int, error)
	GetCode(ctx context.Context, address string, blockNumber *big.Int) ([]byte, error)
	GetBlockByNumber(ctx context.Context, number *big.Int, includeTxs bool) (*Block, error)
	GetTransactionByHash(ctx context.Context, hash string) (*Transaction, error)
	GetSnapshot(ctx context.Context) (*Snapshot, error)
	GetID(ctx context.Context) (*ID, error)
	GetTransactionReceipt(ctx context.Context, hash common.Hash) (*Receipt, error)
	GetChainID(ctx context.Context) (*big.Int, error)
	GetNetworkID(ctx context.Context) (*big.Int, error)
	// GetGasPrice returns a suggested gas price.
	GetGasPrice(ctx context.Context) (*big.Int, error)
	// GetPendingTransactionCount returns the transaction count including pending txs.
	// This value is also the next legal nonce.
	GetPendingTransactionCount(ctx context.Context, account common.Address) (uint64, error)
	SendTransaction(ctx context.Context, tx *Transaction) error
	EthCall(ctx context.Context, msg CallMsg) ([]byte, error)
	Close()
}

func NewClient(url string) (Client, error) {
	r, err := rpc.Dial(url)
	if err != nil {
		return nil, err
	}
	return &client{r: r}, nil
}

type client struct {
	r *rpc.Client
}

func (c *client) Close() {
	c.r.Close()
}

func (c *client) EthCall(ctx context.Context, msg CallMsg) ([]byte, error) {
	var result hexutil.Bytes
	err := c.r.CallContext(ctx, &result, "eth_call", toCallArg(msg), "latest")
	if err != nil {
		return nil, err
	}
	return result, err
}

func (c *client) GetBalance(ctx context.Context, address string, blockNumber *big.Int) (*big.Int, error) {
	var result hexutil.Big
	err := c.r.CallContext(ctx, &result, "eth_getBalance", common.HexToAddress(address), toBlockNumArg(blockNumber))
	return (*big.Int)(&result), err
}

func (c *client) GetCode(ctx context.Context, address string, blockNumber *big.Int) ([]byte, error) {
	var result hexutil.Bytes
	err := c.r.CallContext(ctx, &result, "eth_getCode", common.HexToAddress(address), toBlockNumArg(blockNumber))
	return result, err
}

func (c *client) GetBlockByNumber(ctx context.Context, number *big.Int, includeTxs bool) (*Block, error) {
	return c.getBlock(ctx, "eth_getBlockByNumber", toBlockNumArg(number), includeTxs)
}

func (c *client) GetTransactionByHash(ctx context.Context, hash string) (*Transaction, error) {
	var tx *Transaction
	err := c.r.CallContext(ctx, &tx, "eth_getTransactionByHash", hash)
	if err != nil {
		return nil, err
	} else if tx == nil {
		return nil, NotFoundErr
	} else if tx.R == (common.Hash{}) {
		return nil, fmt.Errorf("server returned transaction without signature")
	}
	return tx, nil
}

func (c *client) GetSnapshot(ctx context.Context) (*Snapshot, error) {
	var s Snapshot
	err := c.r.CallContext(ctx, &s, "clique_getSnapshot", "latest")
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *client) GetID(ctx context.Context) (*ID, error) {
	var block Block
	var netIDStr string
	chainID := new(hexutil.Big)
	batch := []rpc.BatchElem{
		{Method: "eth_getBlockByNumber", Args: []interface{}{"0x0", false}, Result: &block},
		{Method: "net_version", Result: &netIDStr},
		{Method: "eth_chainId", Result: chainID},
	}
	if err := c.r.BatchCallContext(ctx, batch); err != nil {
		return nil, err
	}
	for _, e := range batch {
		if e.Error != nil {
			log.Printf("Method %q failed: %v\n", e.Method, e.Error)
		}
	}
	netID := new(big.Int)
	if _, ok := netID.SetString(netIDStr, 10); !ok {
		return nil, fmt.Errorf("invalid net_version result %q", netIDStr)
	}
	return &ID{NetworkID: netID, ChainID: (*big.Int)(chainID), GenesisHash: block.Hash}, nil
}

func (c *client) GetNetworkID(ctx context.Context) (*big.Int, error) {
	version := new(big.Int)
	var ver string
	if err := c.r.CallContext(ctx, &ver, "net_version"); err != nil {
		return nil, err
	}
	if _, ok := version.SetString(ver, 10); !ok {
		return nil, fmt.Errorf("invalid net_version result %q", ver)
	}
	return version, nil
}

func (c *client) GetChainID(ctx context.Context) (*big.Int, error) {
	var result hexutil.Big
	err := c.r.CallContext(ctx, &result, "eth_chainId")
	return (*big.Int)(&result), err
}

func (c *client) GetTransactionReceipt(ctx context.Context, hash common.Hash) (*Receipt, error) {
	var r *Receipt
	err := c.r.CallContext(ctx, &r, "eth_getTransactionReceipt", hash)
	if err == nil {
		if r == nil {
			return nil, NotFoundErr
		}
	}
	return r, err
}

func (c *client) GetGasPrice(ctx context.Context) (*big.Int, error) {
	var hex hexutil.Big
	if err := c.r.CallContext(ctx, &hex, "eth_gasPrice"); err != nil {
		return nil, err
	}
	return (*big.Int)(&hex), nil
}

func (c *client) GetPendingTransactionCount(ctx context.Context, account common.Address) (uint64, error) {
	return c.getTransactionCount(ctx, account, "pending")
}

func (c *client) getTransactionCount(ctx context.Context, account common.Address, blockNumArg string) (uint64, error) {
	var result hexutil.Uint64
	err := c.r.CallContext(ctx, &result, "eth_getTransactionCount", account, blockNumArg)
	return uint64(result), err
}

func (c *client) SendTransaction(ctx context.Context, tx *Transaction) error {
	data, err := rlp.EncodeToBytes(tx)
	if err != nil {
		return err
	}
	return c.r.CallContext(ctx, nil, "eth_sendRawTransaction", common.ToHex(data))
}

func (c *client) getBlock(ctx context.Context, method string, hashOrNum string, includeTxs bool) (*Block, error) {
	var raw json.RawMessage
	err := c.r.CallContext(ctx, &raw, method, hashOrNum, includeTxs)
	if err != nil {
		return nil, err
	} else if len(raw) == 0 {
		return nil, NotFoundErr
	}
	var block Block
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal json response: %v", err)
	}
	// Quick-verify transaction and uncle lists. This mostly helps with debugging the server.
	if block.Sha3Uncles == types.EmptyUncleHash && len(block.Uncles) > 0 {
		return nil, fmt.Errorf("server returned non-empty uncle list but block header indicates no uncles")
	}
	if block.Sha3Uncles != types.EmptyUncleHash && len(block.Uncles) == 0 {
		return nil, fmt.Errorf("server returned empty uncle list but block header indicates uncles")
	}
	if block.TxsRoot == types.EmptyRootHash && len(block.Txs) > 0 {
		return nil, fmt.Errorf("server returned non-empty transaction list but block header indicates no transactions")
	}
	if block.TxsRoot != types.EmptyRootHash && len(block.TxsRoot) == 0 {
		return nil, fmt.Errorf("server returned empty transaction list but block header indicates transactions")
	}
	// Load uncles because they are not included in the block response.
	var uncles []*types.Header
	if len(block.Uncles) > 0 {
		uncles = make([]*types.Header, len(block.Uncles))
		reqs := make([]rpc.BatchElem, len(block.Uncles))
		for i := range reqs {
			reqs[i] = rpc.BatchElem{
				Method: "eth_getUncleByBlockHashAndIndex",
				Args:   []interface{}{block.Hash, hexutil.EncodeUint64(uint64(i))},
				Result: &uncles[i],
			}
		}
		if err := c.r.BatchCallContext(ctx, reqs); err != nil {
			return nil, err
		}
		for i := range reqs {
			if reqs[i].Error != nil {
				return nil, reqs[i].Error
			}
			if uncles[i] == nil {
				return nil, fmt.Errorf("got null header for uncle %d of block %x", i, block.Hash[:])
			}
		}
	}
	return &block, nil
}

func toBlockNumArg(number *big.Int) string {
	if number == nil {
		return "latest"
	}
	return hexutil.EncodeBig(number)
}

func toCallArg(msg CallMsg) interface{} {
	arg := map[string]interface{}{
		"from": msg.From,
		"to":   msg.To,
	}
	if len(msg.Data) > 0 {
		arg["data"] = hexutil.Bytes(msg.Data)
	}
	if msg.Value != nil {
		arg["value"] = (*hexutil.Big)(msg.Value)
	}
	if msg.Gas != 0 {
		arg["gas"] = hexutil.Uint64(msg.Gas)
	}
	if msg.GasPrice != nil {
		arg["gasPrice"] = (*hexutil.Big)(msg.GasPrice)
	}
	return arg
}
