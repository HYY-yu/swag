package swag

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"text/tabwriter"
)

const SplitTag = "&*"

type Formater struct {
	// debugging output goes here
	debug Debugger

	// excludes excludes dirs and files in SearchDir
	excludes map[string]bool
}

func NewFormater() *Formater {
	formater := &Formater{
		debug: log.New(os.Stdout, "", log.LstdFlags),
	}
	return formater
}

func (f *Formater) FormatAPI(searchDir, excludeDir string) error {
	searchDirs := strings.Split(searchDir, ",")
	for _, searchDir := range searchDirs {
		if _, err := os.Stat(searchDir); os.IsNotExist(err) {
			return fmt.Errorf("dir: %s does not exist", searchDir)
		}
	}
	for _, fi := range strings.Split(excludeDir, ",") {
		fi = strings.TrimSpace(fi)
		if fi != "" {
			fi = filepath.Clean(fi)
			f.excludes[fi] = true
		}
	}

	err := f.formatMultiSearchDir(searchDirs)
	if err != nil {
		return err
	}

	return nil
}

func (f *Formater) formatMultiSearchDir(searchDirs []string) error {
	for _, searchDir := range searchDirs {
		f.debug.Printf("Format API Info, search dir:%s", searchDir)

		err := filepath.Walk(searchDir, f.visit)
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *Formater) visit(path string, fileInfo os.FileInfo, err error) error {
	if err := f.skip(path, fileInfo); err != nil {
		return err
	} else if fileInfo.IsDir() {
		// skip if file is folder
		return nil
	}

	if strings.HasSuffix(strings.ToLower(path), "_test.go") || filepath.Ext(path) != ".go" {
		// skip if file not has suffix "*.go"
		return nil
	}

	err = f.FormatFile(path)
	if err != nil {
		return fmt.Errorf("ParseFile error:%+v", err)
	}
	return nil
}

// skip skip folder in ('vendor' 'docs' 'excludes' 'hidden folder')
func (f *Formater) skip(path string, fileInfo os.FileInfo) error {
	if fileInfo.IsDir() {
		if fileInfo.Name() == "vendor" || // ignore "vendor"
			fileInfo.Name() == "docs" || // exclude docs
			len(fileInfo.Name()) > 1 && fileInfo.Name()[0] == '.' { // exclude all hidden folder
			return filepath.SkipDir
		}

		if f.excludes != nil {
			if _, ok := f.excludes[path]; ok {
				return filepath.SkipDir
			}
		}
	}
	return nil
}

func (formater *Formater) FormatFile(filepath string) error {
	fileSet := token.NewFileSet()
	astFile, err := goparser.ParseFile(fileSet, filepath, nil, goparser.ParseComments)
	if err != nil {
		return fmt.Errorf("cannot format file, err: %w path : %s ", err, filepath)
	}

	var (
		formatedComments = bytes.Buffer{}
		// CommentCache
		oldCommentsMap = make(map[string]string)
	)

	for _, astDescription := range astFile.Decls {
		astDeclaration, ok := astDescription.(*ast.FuncDecl)
		if ok && astDeclaration.Doc != nil && astDeclaration.Doc.List != nil {
			formatFuncDoc(astDeclaration.Doc.List, &formatedComments, oldCommentsMap)
		}
	}

	// Replace the file
	// Read the file
	srcBytes, err := ioutil.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("cannot open file, err: %w path : %s ", err, filepath)
	}
	replaceSrc := string(srcBytes)
	newComments := strings.Split(formatedComments.String(), "\n")
	for _, e := range newComments {
		commentSplit := strings.Split(e, SplitTag)
		if len(commentSplit) == 2 {
			commentHash, commentContent := commentSplit[0], commentSplit[1]

			if !isBlankComment(commentContent) {
				oldComment := oldCommentsMap[commentHash]
				if strings.Contains(replaceSrc, oldComment) {
					replaceSrc = strings.Replace(replaceSrc, oldComment, commentContent, 1)
				}
			}
		}
	}
	return writeBack(filepath, []byte(replaceSrc), srcBytes)
}

func formatFuncDoc(commentList []*ast.Comment, formatedComments *bytes.Buffer, oldCommentsMap map[string]string) {
	tabw := tabwriter.NewWriter(formatedComments, 0, 0, 3, ' ', 0)

	for _, comment := range commentList {
		commentLine := comment.Text
		if isSwagComment(commentLine) || isBlankComment(commentLine) {
			cmd5 := fmt.Sprintf("%x", md5.Sum([]byte(commentLine)))

			// Find the separator and replace to \t
			c := separatorFinder(commentLine)
			oldCommentsMap[cmd5] = commentLine

			// md5 + SplitTag + srcCommentLine
			// eg. xxx&*@Description get struct array
			fmt.Fprintln(tabw, cmd5+SplitTag+c)
		}
	}
	// format by tabwriter
	tabw.Flush()
}

// Check of @Param @Success @Failure @Response @Header
var specialTagForSplit = map[string]byte{
	"@param":    1,
	"@success":  1,
	"@failure":  1,
	"@response": 1,
	"@header":   1,
}

var skipChar = map[byte]byte{
	'"': 1,
	'(': 1,
	'{': 1,
}

var skipCharEnd = map[byte]byte{
	'"': 1,
	')': 1,
	'}': 1,
}

func separatorFinder(comment string) string {
	commentBytes := []byte(comment)
	commentLine := strings.TrimSpace(strings.TrimLeft(comment, "/"))
	if len(commentLine) == 0 {
		return ""
	}
	attribute := strings.Fields(commentLine)[0]
	attrLen := strings.Index(comment, attribute) + len(attribute)
	attribute = strings.ToLower(attribute)
	var i = attrLen

	if _, ok := specialTagForSplit[attribute]; ok {
		var skipFlag bool
		for ; i < len(commentBytes); i++ {
			if !skipFlag && commentBytes[i] == ' ' {
				j := i
				for ; j < len(commentBytes); j++ {
					if commentBytes[j] != ' ' {
						break
					}
				}
				commentBytes = replaceRange(commentBytes, i, j, '\t')
			}
			if _, ok := skipChar[commentBytes[i]]; ok {
				skipFlag = true
				continue
			}
			if skipFlag {
				if _, ok := skipCharEnd[commentBytes[i]]; ok {
					skipFlag = false
				}
			}
		}
	} else {
		for ; i < len(commentBytes); i++ {
			if commentBytes[i] != ' ' {
				break
			}
		}
		if i >= len(commentBytes) {
			return comment
		}
		commentBytes = replaceRange(commentBytes, attrLen, i, '\t')
	}
	return string(commentBytes)
}

func replaceRange(s []byte, start, end int, new byte) []byte {
	if start > end || end < 1 || end > len(s) {
		return s
	}
	s = append(s[:start], s[end-1:]...)
	s[start] = new
	return s
}

func isSwagComment(comment string) bool {
	lc := strings.ToLower(comment)
	return regexp.MustCompile("@[A-z]+").MatchString(lc)
}

func isBlankComment(comment string) bool {
	lc := strings.TrimSpace(comment)
	return len(lc) == 0
}

// writeBack write to file
func writeBack(filepath string, src, old []byte) error {
	// make a temporary backup before overwriting original
	bakname, err := backupFile(filepath+".", old, 0644)
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(filepath, src, 0644)
	if err != nil {
		os.Rename(bakname, filepath)
		return err
	}
	err = os.Remove(bakname)
	if err != nil {
		return err
	}
	return nil
}

const chmodSupported = runtime.GOOS != "windows"

// backupFile writes data to a new file named filename<number> with permissions perm,
// with <number randomly chosen such that the file name is unique. backupFile returns
// the chosen file name.
// copy from golang/cmd/gofmt
func backupFile(filename string, data []byte, perm os.FileMode) (string, error) {
	// create backup file
	f, err := ioutil.TempFile(filepath.Dir(filename), filepath.Base(filename))
	if err != nil {
		return "", err
	}
	bakname := f.Name()
	if chmodSupported {
		err = f.Chmod(perm)
		if err != nil {
			f.Close()
			os.Remove(bakname)
			return bakname, err
		}
	}

	// write data to backup file
	_, err = f.Write(data)
	if err1 := f.Close(); err == nil {
		err = err1
	}
	return bakname, err
}