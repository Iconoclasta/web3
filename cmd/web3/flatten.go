package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

var pragma string

type importRec struct {
	FullPath  string
	Code      []string
	Uses      map[string]bool
	Created   bool // has been depended on or processed
	Processed bool // has been processed for includes
	Resolved  bool // all includes have been resolved
	Written   bool // written out
}

var imports map[string]importRec

func extractFilePath(line string) string {
	line = strings.Replace(line, "import ", "", 2)
	line = strings.Replace(line, "\"", "", 2)
	line = strings.Replace(line, ";", "", 2)
	return strings.TrimSpace(filepath.Clean(line))
}

func loadAndSplitFile(fileName string) (newFiles bool, err error) {
	thisPath := filepath.Dir(fileName)
	shortName := filepath.Base(fileName)
	if imports[shortName].Processed {
		return
	}
	thisRec := importRec{FullPath: fileName, Created: true, Uses: make(map[string]bool)}
	data, err := ioutil.ReadFile(fileName)
	contents := string(data)
	lines := strings.Split(contents, "\n")
	noImports := true
	for li, line := range lines {
		if strings.HasPrefix(line, "pragma solidity") {
			pragma = line
			continue
		}
		if strings.HasPrefix(line, "import") {
			noImports = false
			fpath := thisPath + "/" + extractFilePath(line)
			fname := filepath.Base(fpath)
			if !imports[fname].Created {
				newFiles = true
				imports[fname] = importRec{
					FullPath: fpath,
					Created:  true,
					Uses:     make(map[string]bool),
				}
			}
			thisRec.Uses[fname] = true
		}
		if strings.HasPrefix(line, "contract") || strings.HasPrefix(line, "library") || strings.HasPrefix(line, "interface") {
			thisRec.Code = lines[li:]
			break
		}
	}
	thisRec.Processed = true
	imports[shortName] = thisRec
	thisRec.Resolved = noImports
	return
}

func FlattenSourceFile(fName, oName string) (string, error) {
	imports = make(map[string]importRec)
	if oName == "" {
		basename := filepath.Base(fName)
		oName = strings.TrimSuffix(basename, filepath.Ext(basename)) + "_flatten.sol"

	}
	if _, err := os.Stat(fName); err != nil {
		return oName, err
	}
	if _, err := os.Stat(oName); err == nil {
		return oName, errors.New("the output file already exist")
	}
	newFiles, err := loadAndSplitFile(fName)
	if err != nil {
		return oName, err
	}
	if newFiles { //file has imports
		f, _ := os.Create(oName)
		defer f.Close()
		w := bufio.NewWriter(f)
		for {
			repeat := false
			for _, iRec := range imports {
				if iRec.Processed {
					continue
				}
				newFiles, err = loadAndSplitFile(iRec.FullPath)
				if err != nil {
					return oName, err
				}
				repeat = repeat || newFiles
			}
			if !repeat {
				break
			}
		}
		fmt.Fprintln(w, pragma)
		for {
			completed := true
			for key, mp := range imports {
				if mp.Written {
					continue
				}
				completed = false
				if mp.Resolved {
					for _, line := range mp.Code {
						fmt.Fprintln(w, line)
					}
					mp.Written = true
					imports[key] = mp
					continue
				}
				amResolved := true
				for k2 := range mp.Uses {
					if !imports[filepath.Base(k2)].Written {
						amResolved = false
					}
				}
				if amResolved {
					mp.Resolved = true
					imports[key] = mp
					continue
				}
			}
			if completed {
				break
			}
		}
		return oName, w.Flush()
	} //file doesn't have any imports, so just return the same file
	return fName, err

}
