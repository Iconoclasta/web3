package web3

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/gochain/gochain/v3/accounts/abi"
	"github.com/gochain/gochain/v3/common"
	"github.com/gochain/gochain/v3/common/hexutil"
	"github.com/gochain/gochain/v3/core/types"
	"github.com/gochain/gochain/v3/crypto"
	"github.com/gochain/gochain/v3/rlp"
)

var NotFoundErr = errors.New("not found")

const (
	testnetURL = "https://testnet-rpc.gochain.io"
	mainnetURL = "https://rpc.gochain.io"
)

var Networks = map[string]Network{
	"testnet": {
		URL:  testnetURL,
		Unit: "GO",
	},
	"gochain": {
		URL:  mainnetURL,
		Unit: "GO",
	},
	"localhost": {
		URL:  "http://localhost:8545",
		Unit: "GO",
	},
	"ethereum": {
		URL:  "https://main-rpc.linkpool.io",
		Unit: "ETH",
	},
	"ropsten": {
		URL:  "https://ropsten-rpc.linkpool.io",
		Unit: "ETH",
	},
}

type Network struct {
	URL  string
	Unit string
	//TODO net_id, chain_id
}

var (
	weiPerGO   = big.NewInt(1e18)
	weiPerGwei = big.NewInt(1e9)
)

// WeiAsBase converts w wei in to the base unit, and formats it as a decimal fraction with full precision (up to 18 decimals).
func WeiAsBase(w *big.Int) string {
	return new(big.Rat).SetFrac(w, weiPerGO).FloatString(18)
}

// WeiAsGwei converts w wei in to gwei, and formats it as a decimal fraction with full precision (up to 9 decimals).
func WeiAsGwei(w *big.Int) string {
	return new(big.Rat).SetFrac(w, weiPerGwei).FloatString(9)
}

// IntAsFloat converts a *big.Int (ie: wei), to *big.Float (ie: ETH)
func IntAsFloat(i *big.Int, decimals int) *big.Float {
	f := new(big.Float)
	f.SetPrec(100)
	f.SetInt(i)
	f.Quo(f, big.NewFloat(math.Pow10(decimals)))
	return f
}

// FloatAsInt converts a float to a *big.Int based on the decimals passed in
func FloatAsInt(amountF *big.Float, decimals int) *big.Int {
	bigval := new(big.Float)
	bigval.SetPrec(100)
	bigval.SetString(amountF.String()) // have to do this to not lose precision

	coinDecimals := new(big.Float)
	coinDecimals.SetFloat64(math.Pow10(decimals))
	bigval.Mul(bigval, coinDecimals)

	amountI := new(big.Int)
	// todo: could sanity check the accuracy here
	bigval.Int(amountI) // big.NewInt(int64(amountInWeiF)) // amountInGo.Mul(amountInGo, big.NewInt(int64(math.Pow10(18))))
	return amountI
}

// CallConstantFunction executes a contract function call without submitting a transaction.
func CallConstantFunction(ctx context.Context, client Client, myabi abi.ABI, address string, functionName string, params ...interface{}) ([]interface{}, error) {
	if address == "" {
		return nil, errors.New("no contract address specified")
	}
	fn := myabi.Methods[functionName]
	goParams, err := ConvertArguments(fn.Inputs, params)
	if err != nil {
		return nil, err
	}
	input, err := myabi.Pack(functionName, goParams...)
	if err != nil {
		return nil, fmt.Errorf("failed to pack values: %v", err)
	}
	toAddress := common.HexToAddress(address)
	res, err := client.Call(ctx, CallMsg{Data: input, To: &toAddress})
	if err != nil {
		return nil, err
	}
	vals, err := fn.Outputs.UnpackValues(res)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack values from %s: %v", hexutil.Encode(res), err)
	}
	return convertOutputParams(vals), nil
}

// CallTransactFunction submits a transaction to execute a smart contract function call.
func CallTransactFunction(ctx context.Context, client Client, myabi abi.ABI, address, privateKeyHex, functionName string, amount int, params ...interface{}) (*Transaction, error) {
	if address == "" {
		return nil, errors.New("no contract address specified")
	}
	fn := myabi.Methods[functionName]
	goParams, err := ConvertArguments(fn.Inputs, params)
	if err != nil {
		return nil, err
	}
	input, err := myabi.Pack(functionName, goParams...)
	if err != nil {
		return nil, fmt.Errorf("failed to pack values: %v", err)
	}
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %v", err)
	}
	gasPrice, err := client.GetGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot get gas price: %v", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("error casting public key to ECDSA")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	nonce, err := client.GetPendingTransactionCount(ctx, fromAddress)
	if err != nil {
		return nil, fmt.Errorf("cannot get nonce: %v", err)
	}
	toAddress := common.HexToAddress(address)
	tx := types.NewTransaction(nonce, toAddress, big.NewInt(int64(amount)), 20000000, gasPrice, input)
	signedTx, err := types.SignTx(tx, types.HomesteadSigner{}, privateKey)
	if err != nil {
		return nil, fmt.Errorf("cannot sign transaction: %v", err)
	}
	raw, err := rlp.EncodeToBytes(signedTx)
	if err != nil {
		return nil, err
	}
	err = client.SendRawTransaction(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("cannot send transaction: %v", err)
	}
	return convertTx(signedTx, fromAddress), nil
}

// DeployContract submits a contract creation transaction.
// abiJSON is only required when including params for the constructor.
func DeployContract(ctx context.Context, client Client, privateKeyHex string, binHex, abiJSON string, params ...interface{}) (*Transaction, error) {
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %v", err)
	}

	gasPrice, err := client.GetGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot get gas price: %v", err)
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("error casting public key to ECDSA")
	}

	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	nonce, err := client.GetPendingTransactionCount(ctx, fromAddress)
	if err != nil {
		return nil, fmt.Errorf("cannot get nonce: %v", err)
	}
	binData, err := hexutil.Decode(binHex)
	if err != nil {
		return nil, fmt.Errorf("cannot decode contract data: %v", err)
	}
	if len(params) > 0 {
		abiData, err := abi.JSON(strings.NewReader(abiJSON))
		if err != nil {
			return nil, fmt.Errorf("failed to parse ABI: %v", err)
		}
		goParams, err := ConvertArguments(abiData.Constructor.Inputs, params)
		if err != nil {
			return nil, err
		}
		input, err := abiData.Pack("", goParams...)
		if err != nil {
			return nil, fmt.Errorf("cannot pack parameters: %v", err)
		}
		binData = append(binData, input...)
	}
	//TODO try to use web3.Transaction only; can't sign currently
	tx := types.NewContractCreation(nonce, big.NewInt(0), 2000000, gasPrice, binData)
	signedTx, err := types.SignTx(tx, types.HomesteadSigner{}, privateKey)
	if err != nil {
		return nil, fmt.Errorf("cannot sign transaction: %v", err)
	}
	raw, err := rlp.EncodeToBytes(signedTx)
	if err != nil {
		return nil, err
	}
	err = client.SendRawTransaction(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("cannot send transaction: %v", err)
	}

	return convertTx(signedTx, fromAddress), nil
}

func Send(ctx context.Context, client Client, privateKeyHex string, address common.Address, amount *big.Int) (*Transaction, error) {
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %v", err)
	}
	gasPrice, err := client.GetGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot get gas price: %v", err)
	}
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("error casting public key to ECDSA")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	nonce, err := client.GetPendingTransactionCount(ctx, fromAddress)
	if err != nil {
		return nil, fmt.Errorf("cannot get nonce: %v", err)
	}
	tx := types.NewTransaction(nonce, address, amount, 100000, gasPrice, nil)
	signedTx, err := types.SignTx(tx, types.HomesteadSigner{}, privateKey)
	if err != nil {
		return nil, fmt.Errorf("cannot sign transaction: %v", err)
	}
	raw, err := rlp.EncodeToBytes(signedTx)
	if err != nil {
		return nil, err
	}
	err = client.SendRawTransaction(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("cannot send transaction: %v", err)
	}
	return convertTx(signedTx, fromAddress), nil
}

func convertTx(tx *types.Transaction, from common.Address) *Transaction {
	rtx := &Transaction{}
	rtx.Nonce = tx.Nonce()
	rtx.GasPrice = tx.GasPrice()
	rtx.GasLimit = tx.Gas()
	rtx.To = tx.To()
	rtx.Value = tx.Value()
	rtx.Input = tx.Data()
	rtx.Hash = tx.Hash()
	rtx.From = from
	rtx.V, rtx.R, rtx.S = tx.RawSignatureValues()
	return rtx
}

// ConvertArguments attempts to convert each param to the matching args type.
// Unrecognized param types are passed through unmodified.
//
// Note: The encoding/json package uses float64 for numbers by default, which is inaccurate
// for many web3 types, and unsupported here. The json.Decoder method UseNumber() will
// switch to using json.Number instead, which is accurate (full precision, backed by the
// original string) and supported here.
func ConvertArguments(args abi.Arguments, params []interface{}) ([]interface{}, error) {
	if len(args) != len(params) {
		return nil, fmt.Errorf("mismatched argument (%d) and parameter (%d) counts", len(args), len(params))
	}
	var convertedParams []interface{}
	for i, input := range args {
		param, err := convertArgument(input.Type.T, input.Type.Size, params[i])
		if err != nil {
			return nil, err
		}
		convertedParams = append(convertedParams, param)
	}
	return convertedParams, nil
}

// convertArgument attempts to convert param to the ABI type and size.
// Unrecognized param types are passed through unmodified.
func convertArgument(abiType byte, size int, param interface{}) (interface{}, error) {
	// fmt.Println("INPUT TYPE:", t.T, "SIZE:", t.Size)
	switch abiType {
	case abi.StringTy:
	case abi.BoolTy:
		if s, ok := param.(string); ok {
			val, err := strconv.ParseBool(s)
			if err != nil {
				return nil, fmt.Errorf("failed to parse bool %q: %v", s, err)
			}
			return val, nil
		}
	case abi.UintTy, abi.IntTy:
		if j, ok := param.(json.Number); ok {
			param = string(j)
		}
		if s, ok := param.(string); ok {
			val, ok := new(big.Int).SetString(s, 0)
			if !ok {
				return nil, fmt.Errorf("failed to parse big.Int: %s", s)
			}
			return convertInt(abiType == abi.IntTy, size, val)
		} else if i, ok := param.(*big.Int); ok {
			return convertInt(abiType == abi.IntTy, size, i)
		}
		v := reflect.ValueOf(param)
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			i := new(big.Int).SetInt64(v.Int())
			return convertInt(abiType == abi.IntTy, size, i)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			i := new(big.Int).SetUint64(v.Uint())
			return convertInt(abiType == abi.IntTy, size, i)
		case reflect.Float64, reflect.Float32:
			return nil, fmt.Errorf("floating point numbers are not valid in web3 - please use an integer or string instead (including big.Int and json.Number)")
		}
	case abi.AddressTy:
		if s, ok := param.(string); ok {
			if !common.IsHexAddress(s) {
				return nil, fmt.Errorf("invalid hex address: %s", s)
			}
			return common.HexToAddress(s), nil
		}
	case abi.BytesTy:
		if s, ok := param.(string); ok {
			val, err := hexutil.Decode(s)
			if err != nil {
				return nil, fmt.Errorf("failed to parse bytes %q: %v", s, err)
			}
			return val, nil
		}
	case abi.HashTy:
		if s, ok := param.(string); ok {
			val, err := hexutil.Decode(s)
			if err != nil {
				return nil, fmt.Errorf("failed to parse hash %q: %v", s, err)
			}
			if len(val) != common.HashLength {
				return nil, fmt.Errorf("invalid hash length %d:hash must be 32 bytes", len(val))
			}
			return common.BytesToHash(val), nil
		}
	case abi.FixedBytesTy:
		switch {
		case size == 32:
			if s, ok := param.(string); ok {
				val, err := hexutil.Decode(s)
				if err != nil {
					return nil, fmt.Errorf("failed to parse hash %q: %v", s, err)
				}
				if len(val) != common.HashLength {
					return nil, fmt.Errorf("invalid hash length %d:hash must be 32 bytes", len(val))
				}
				return common.BytesToHash(val), nil
			}
		default:
			return nil, fmt.Errorf("unsupported input byte array size %v", size)
		}
	default:
		return nil, fmt.Errorf("unsupported input type %v", abiType)
	}
	return param, nil
}

func convertOutputParams(params []interface{}) []interface{} {
	for i := range params {
		p := params[i]
		if h, ok := p.(common.Hash); ok {
			params[i] = h
		} else if a, ok := p.(common.Address); ok {
			params[i] = a
		} else if b, ok := p.(hexutil.Bytes); ok {
			params[i] = b
		} else if v := reflect.ValueOf(p); v.Kind() == reflect.Array {
			if t := v.Type(); t.Elem().Kind() == reflect.Uint8 {
				b := make([]byte, t.Len())
				bv := reflect.ValueOf(b)
				// Copy since we can't t.Slice() unaddressable arrays.
				for i := 0; i < t.Len(); i++ {
					bv.Index(i).Set(v.Index(i))
				}
				params[i] = hexutil.Bytes(b)
			}
		}
	}
	return params
}

//TODO Deprecated: prefer built-in UnpackValues() func and convertOutputParams.
func convertOutputParameter(t abi.Argument) (interface{}, error) {
	switch t.Type.T {
	case abi.BoolTy:
		return new(bool), nil
	case abi.UintTy:
		switch size := t.Type.Size; {
		case size > 64:
			i := new(big.Int)
			return &i, nil
		case size > 32:
			return new(uint64), nil
		case size > 16:
			return new(uint32), nil
		case size > 8:
			return new(uint16), nil
		default:
			return new(uint8), nil
		}
	case abi.IntTy:
		switch size := t.Type.Size; {
		case size > 64:
			i := new(big.Int)
			return &i, nil
		case size > 32:
			return new(int64), nil
		case size > 16:
			return new(int32), nil
		case size > 8:
			return new(int16), nil
		default:
			return new(int8), nil
		}
	case abi.StringTy:
		return new(string), nil
	case abi.AddressTy:
		return new(common.Address), nil
	case abi.BytesTy:
		return new(hexutil.Bytes), nil
	case abi.FixedBytesTy:
		switch size := t.Type.Size; {
		case size == 32:
			return new(common.Hash), nil
		default:
			return nil, fmt.Errorf("unsupported output byte array size %v", size)
		}
	default:
		return nil, fmt.Errorf("unsupported output type %v", t.Type.T)
	}
}

// convertInt converts a big.Int in to the provided type.
func convertInt(signed bool, size int, i *big.Int) (interface{}, error) {
	if signed {
		switch {
		case size > 64:
			return i, nil
		case size > 32:
			if !i.IsInt64() {
				return nil, fmt.Errorf("integer overflows int64: %s", i)
			}
			return i.Int64(), nil
		case size > 16:
			if !i.IsInt64() || i.Int64() > math.MaxInt32 {
				return nil, fmt.Errorf("integer overflows int32: %s", i)
			}
			return int32(i.Int64()), nil
		case size > 8:
			if !i.IsInt64() || i.Int64() > math.MaxInt16 {
				return nil, fmt.Errorf("integer overflows int16: %s", i)
			}
			return int16(i.Int64()), nil
		default:
			if !i.IsInt64() || i.Int64() > math.MaxInt8 {
				return nil, fmt.Errorf("integer overflows int8: %s", i)
			}
			return int8(i.Int64()), nil
		}
	} else {
		switch {
		case size > 64:
			if i.Sign() == -1 {
				return nil, fmt.Errorf("negative value in unsigned field: %s", i)
			}
			return i, nil
		case size > 32:
			if !i.IsUint64() {
				return nil, fmt.Errorf("integer overflows uint64: %s", i)
			}
			return i.Uint64(), nil
		case size > 16:
			if !i.IsUint64() || i.Uint64() > math.MaxUint32 {
				return nil, fmt.Errorf("integer overflows uint32: %s", i)
			}
			return uint32(i.Uint64()), nil
		case size > 8:
			if !i.IsUint64() || i.Uint64() > math.MaxUint16 {
				return nil, fmt.Errorf("integer overflows uint16: %s", i)
			}
			return uint16(i.Uint64()), nil
		default:
			if !i.IsUint64() || i.Uint64() > math.MaxUint8 {
				return nil, fmt.Errorf("integer overflows uint8: %s", i)
			}
			return uint8(i.Uint64()), nil
		}
	}
}

// WaitForReceipt polls for a transaction receipt until it is available, or ctx is cancelled.
func WaitForReceipt(ctx context.Context, client Client, hash common.Hash) (*Receipt, error) {
	for {
		receipt, err := client.GetTransactionReceipt(ctx, hash)
		if err == nil {
			return receipt, nil
		}
		if err != NotFoundErr {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func FindEventById(abi abi.ABI, id common.Hash) *abi.Event {
	for _, event := range abi.Events {
		if event.Id() == id {
			return &event
		}
	}
	return nil
}
func getInputs(args abi.Arguments, indexed bool) []abi.Argument {
	var out []abi.Argument
	for _, arg := range args {
		if arg.Indexed == indexed {
			out = append(out, arg)
		}
	}
	return out
}

// func ParseReceipt(myabi abi.ABI, receipt *Receipt) (map[string]map[string]interface{}, error) {
func ParseLogs(myabi abi.ABI, logs []*types.Log) ([]Event, error) {
	var output []Event
	// output := make(map[string]map[string]interface{})
	for _, log := range logs {
		var out []interface{}
		//event id is always in the first topic
		event := FindEventById(myabi, log.Topics[0])
		fields := make(map[string]interface{})
		//TODO use event.Inputs.UnpackIntoMap instead when available
		nonIndexed := getInputs(event.Inputs, false)
		for _, t := range nonIndexed {
			x, err := convertOutputParameter(t)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		if len(nonIndexed) > 1 {
			err := myabi.Unpack(&out, event.Name, log.Data)
			if err != nil {
				return nil, err
			}
			for i, o := range out {
				fields[nonIndexed[i].Name] = reflect.ValueOf(o).Elem().Interface()
			}
		} else {
			err := myabi.Unpack(&out[0], event.Name, log.Data)
			if err != nil {
				return nil, err
			}
			fields[nonIndexed[0].Name] = out[0]
		}
		for i, input := range getInputs(event.Inputs, true) {
			fields[input.Name] = log.Topics[i+1].String()
		}
		output = append(output, Event{Name: event.Name, Fields: fields})
	}
	return output, nil
}

// ParseAmount parses a string (human readable amount with units ie 1go, 1nanogo...) and returns big.Int value of this string in wei/atto
func ParseAmount(amount string) (*big.Int, error) {
	var ret = new(big.Int)
	var mul = big.NewInt(1)
	amount = strings.ToLower(amount)
	switch {
	case strings.HasSuffix(amount, "nanogo"):
		amount = strings.TrimSuffix(amount, "nanogo")
		mul = weiPerGwei
	case strings.HasSuffix(amount, "gwei"):
		amount = strings.TrimSuffix(amount, "gwei")
		mul = weiPerGwei
	case strings.HasSuffix(amount, "attogo"):
		amount = strings.TrimSuffix(amount, "attogo")
	case strings.HasSuffix(amount, "wei"):
		amount = strings.TrimSuffix(amount, "wei")
	case strings.HasSuffix(amount, "eth"):
		amount = strings.TrimSuffix(amount, "eth")
		mul = weiPerGO
	default:
		amount = strings.TrimSuffix(amount, "go")
		mul = weiPerGO
	}
	val, err := ParseBigInt(amount)
	if err != nil {
		return nil, err
	}
	return ret.Mul(val, mul), nil
}

// ParseBigInt parses a string (base 10 only) and returns big.Int value of this string in wei/atto
func ParseBigInt(value string) (*big.Int, error) {
	if value == "" {
		return nil, errors.New("Cannot parse empty string")
	}
	i, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, fmt.Errorf("Failed to parse integer %q", value)
	}
	return i, nil
}
