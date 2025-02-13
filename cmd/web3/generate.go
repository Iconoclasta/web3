package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/gochain/gochain/v3/accounts/abi/bind"
	"github.com/gochain/web3/assets"
	"github.com/urfave/cli"
	"html/template"
	"io/ioutil"
	"log"
	"math/big"
	"os"
	"os/exec"
)

func GenerateCode(ctx context.Context, c *cli.Context) {
	var lang bind.Lang
	switch c.String("lang") {
	case "go":
		lang = bind.LangGo
	case "java":
		lang = bind.LangJava
	case "objc":
		lang = bind.LangObjC
	default:
		fatalExit(fmt.Errorf("Unsupported destination language: %v", lang))
	}

	abiFile := c.String("abi")

	if abiFile == "" {
		fatalExit(errors.New("Please set the ABI file name"))
	}

	abi, err := ioutil.ReadFile(abiFile)
	if err != nil {
		fatalExit(fmt.Errorf("Failed to read file %q: %v", abiFile, err))
	}

	abis := []string{string(abi)}
	bins := []string{c.String("")}
	types := []string{c.String("pkg")}

	code, err := bind.Bind(types, abis, bins, c.String("pkg"), lang)
	if err != nil {
		fatalExit(fmt.Errorf("Failed to generate ABI binding %q: %v", abiFile, err))
	}
	outFile := c.String("out")

	if err := ioutil.WriteFile(outFile, []byte(code), 0600); err != nil {
		fatalExit(fmt.Errorf("Failed to write ABI binding %q: %v", abiFile, err))
	}
	fmt.Println("The generated code has been successfully written to", outFile, "file")
}

func GenerateContract(ctx context.Context, contractType string, c *cli.Context) {
	if c.String("symbol") == "" {
		fatalExit(errors.New("Symbol is required"))
	}
	if c.String("name") == "" {
		fatalExit(errors.New("Name is required"))
	}
	if _, err := os.Stat("lib/oz"); os.IsNotExist(err) {
		cmd := exec.Command("git", "clone", "--depth", "1", "--branch", "master", "https://github.com/OpenZeppelin/openzeppelin-solidity", "lib/oz")
		log.Printf("Cloning OpenZeppeling repo...")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			fatalExit(fmt.Errorf("Cloning finished with error: %v", err))
		}
		err = os.RemoveAll("lib/oz/.git")
		if err != nil {
			fatalExit(fmt.Errorf("Cannot cleanup .git dir in lib/oz: %v", err))
		}
	}
	if contractType == "erc20" {
		var capped *big.Int
		decimals := c.Int("decimals")
		if decimals <= 0 {
			fatalExit(errors.New("Decimals should be greater than 0"))
		}
		if c.String("capped") != "" {
			var ok bool
			capped, ok = new(big.Int).SetString(c.String("capped"), 10)
			if !ok {
				fatalExit(errors.New("Cannot parse capped value"))
			}
			if capped.Cmp(big.NewInt(0)) < 1 {
				fatalExit(errors.New("Capped should be greater than 0"))
			}
			capped.Mul(capped, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
		}
		params := assets.Erc20Params{
			Symbol:    c.String("symbol"),
			TokenName: c.String("name"),
			Cap:       capped,
			Pausable:  c.Bool("pausable"),
			Mintable:  c.Bool("mintable"),
			Burnable:  c.Bool("burnable"),
			Decimals:  decimals,
		}
		// TODO: add initial-supply flag
		// TODO: must have initial supply or be mintable, otherwise this is zero
		// TODO: initial supply can be set in constructor given to owner, eg: _mint(msg.sender, initialSupply)
		s, err := assets.GenERC20(ctx, &params)
		if err != nil {
			fatalExit(err)
		}
		writeStringToFile(s, params.Symbol)
	} else if contractType == "erc721" {
		// we're going to assume metadata
		params := assets.Erc721Params{
			Symbol:    c.String("symbol"),
			TokenName: c.String("name"),
			Pausable:  c.Bool("pausable"),
			Mintable:  c.Bool("mintable"),
			Burnable:  c.Bool("burnable"),
		}
		processTemplate(params, params.Symbol, assets.ERC721Template)
	}
}

func processTemplate(params interface{}, fileName, contractTemplate string) {
	tmpl, err := template.New("contract").Parse(contractTemplate)
	if err != nil {
		fatalExit(fmt.Errorf("Cannot parse the template: %v", err))
	}
	var buff bytes.Buffer
	err = tmpl.Execute(&buff, params)
	if err != nil {
		fatalExit(fmt.Errorf("Cannot execute the template: %v", err))
	}
	writeStringToFile(buff.String(), fileName)
}
func writeStringToFile(s, fileName string) {
	err := ioutil.WriteFile(fileName+".sol", []byte(s), 0666)
	if err != nil {
		fatalExit(fmt.Errorf("Cannot create the file: %v", err))
	}
	fmt.Println("The sample contract has been successfully written to", fileName+".sol", "file")
}
