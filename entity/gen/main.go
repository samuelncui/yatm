package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type messageOneofInfo struct {
	messageName   string
	oneofFieldGo  string
	interfaceName string
	methodName    string
}

type wrapperInfo struct {
	parentMessage string
	wrapperType   string
	fieldName     string
	fieldType     string
}

func main() {
	entityDir, err := resolveEntityDir()
	if err != nil {
		panic(err)
	}

	pbFiles, err := findPbGoFiles(entityDir)
	if err != nil {
		panic(err)
	}
	for _, path := range pbFiles {
		messageInfos, wrapperInfos, err := parsePbGoFile(path)
		if err != nil {
			panic(err)
		}

		base := strings.TrimSuffix(filepath.Base(path), ".pb.go")
		outputPath := filepath.Join(entityDir, base+".oneof_gen.go")
		tsOutputPath := filepath.Join(entityDir, "..", "frontend", "src", "entity", base+".oneof_gen.ts")
		output, err := renderOneofCode(messageInfos, wrapperInfos)
		if err != nil {
			panic(err)
		}
		if len(output) == 0 {
			for _, artifact := range []string{outputPath, tsOutputPath} {
				if err := os.Remove(artifact); err != nil && !os.IsNotExist(err) {
					panic(err)
				}
			}
			continue
		}
		if err := os.WriteFile(outputPath, output, 0o644); err != nil {
			panic(err)
		}

		outputTS, err := renderOneofCodeTS(messageInfos, wrapperInfos)
		if err != nil {
			panic(err)
		}
		if len(outputTS) > 0 {
			if err := os.WriteFile(tsOutputPath, outputTS, 0o644); err != nil {
				panic(err)
			}
		}
	}
}

func resolveEntityDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot resolve current file path")
	}
	return filepath.Dir(filepath.Dir(file)), nil
}

func findPbGoFiles(entityDir string) ([]string, error) {
	var results []string
	err := filepath.WalkDir(entityDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".pb.go") {
			results = append(results, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(results)
	return results, nil
}

func parsePbGoFile(path string) (map[string]messageOneofInfo, map[string][]wrapperInfo, error) {
	messageInfos := map[string]messageOneofInfo{}
	wrapperInfos := map[string][]wrapperInfo{}
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}

			if info, ok := extractMessageOneofInfo(typeSpec.Name.Name, structType); ok {
				if _, exists := messageInfos[info.messageName]; !exists {
					messageInfos[info.messageName] = info
				}
			}
			if info, ok := extractWrapperInfo(typeSpec.Name.Name, structType, fset); ok {
				wrapperInfos[info.parentMessage] = append(wrapperInfos[info.parentMessage], info)
			}
		}
	}

	for name := range wrapperInfos {
		// Scalar oneofs use protoc's native wrappers; Go cannot attach methods to built-in types.
		for _, variant := range wrapperInfos[name] {
			if !strings.HasPrefix(variant.fieldType, "*") {
				delete(messageInfos, name)
				break
			}
		}
		sort.Slice(wrapperInfos[name], func(i, j int) bool {
			return wrapperInfos[name][i].wrapperType < wrapperInfos[name][j].wrapperType
		})
	}

	return messageInfos, wrapperInfos, nil
}

func extractMessageOneofInfo(typeName string, structType *ast.StructType) (messageOneofInfo, bool) {
	for _, field := range structType.Fields.List {
		tag, ok := parseStructTag(field.Tag)
		if !ok {
			continue
		}
		if tag.Get("protobuf_oneof") == "" {
			continue
		}
		if len(field.Names) == 0 {
			continue
		}
		oneofFieldGo := field.Names[0].Name
		interfaceName := "Oneof" + typeName
		methodName := "isOneof" + typeName
		return messageOneofInfo{
			messageName:   typeName,
			oneofFieldGo:  oneofFieldGo,
			interfaceName: interfaceName,
			methodName:    methodName,
		}, true
	}
	return messageOneofInfo{}, false
}

func extractWrapperInfo(typeName string, structType *ast.StructType, fset *token.FileSet) (wrapperInfo, bool) {
	if structType.Fields == nil || len(structType.Fields.List) != 1 {
		return wrapperInfo{}, false
	}
	field := structType.Fields.List[0]
	if len(field.Names) == 0 {
		return wrapperInfo{}, false
	}
	tag, ok := parseStructTag(field.Tag)
	if !ok {
		return wrapperInfo{}, false
	}
	if !hasOneofOption(tag.Get("protobuf")) {
		return wrapperInfo{}, false
	}

	fieldName := field.Names[0].Name
	parent := strings.TrimSuffix(typeName, "_"+fieldName)
	if parent == typeName {
		return wrapperInfo{}, false
	}

	return wrapperInfo{
		parentMessage: parent,
		wrapperType:   typeName,
		fieldName:     fieldName,
		fieldType:     exprString(fset, field.Type),
	}, true
}

func parseStructTag(tag *ast.BasicLit) (reflect.StructTag, bool) {
	if tag == nil {
		return "", false
	}
	raw, err := strconv.Unquote(tag.Value)
	if err != nil {
		return "", false
	}
	return reflect.StructTag(raw), true
}

func hasOneofOption(protoTag string) bool {
	if protoTag == "" {
		return false
	}
	parts := strings.Split(protoTag, ",")
	for _, part := range parts {
		if part == "oneof" {
			return true
		}
	}
	return false
}

func exprString(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, expr)
	return buf.String()
}

func renderOneofCode(messages map[string]messageOneofInfo, wrappers map[string][]wrapperInfo) ([]byte, error) {
	var messageNames []string
	for name := range messages {
		if len(wrappers[name]) == 0 {
			continue
		}
		messageNames = append(messageNames, name)
	}
	sort.Strings(messageNames)
	if len(messageNames) == 0 {
		return nil, nil
	}

	var buf bytes.Buffer
	buf.WriteString("package entity\n\n")

	for _, name := range messageNames {
		msg := messages[name]
		wrappersForMsg := wrappers[name]
		buf.WriteString("type " + msg.interfaceName + " interface {\n\t" + msg.methodName + "()\n}\n\n")
		for _, wrapper := range wrappersForMsg {
			buf.WriteString("func (x " + wrapper.fieldType + ") " + msg.methodName + "() {}\n\n")
		}

		buf.WriteString("func (x *" + msg.messageName + ") Unpack() " + msg.interfaceName + " {\n")
		buf.WriteString("\tswitch param := x." + msg.oneofFieldGo + ".(type) {\n")
		for _, wrapper := range wrappersForMsg {
			buf.WriteString("\tcase *" + wrapper.wrapperType + ":\n")
			buf.WriteString("\t\treturn param." + wrapper.fieldName + "\n")
		}
		buf.WriteString("\tdefault:\n\t\treturn nil\n\t}\n}\n\n")

		buf.WriteString("func Pack" + msg.messageName + "(param " + msg.interfaceName + ") *" + msg.messageName + " {\n")
		buf.WriteString("\tswitch param := param.(type) {\n")
		for _, wrapper := range wrappersForMsg {
			buf.WriteString("\tcase " + wrapper.fieldType + ":\n")
			buf.WriteString("\t\treturn &" + msg.messageName + "{\n")
			buf.WriteString("\t\t\t" + msg.oneofFieldGo + ": &" + wrapper.wrapperType + "{\n")
			buf.WriteString("\t\t\t\t" + wrapper.fieldName + ": param,\n")
			buf.WriteString("\t\t\t},\n")
			buf.WriteString("\t\t}\n")
		}
		buf.WriteString("\tdefault:\n\t\treturn nil\n\t}\n}\n\n")

		for _, wrapper := range wrappersForMsg {
			buf.WriteString("func (p " + wrapper.fieldType + ") Pack() *" + msg.messageName + " {\n")
			buf.WriteString("\treturn &" + msg.messageName + "{\n")
			buf.WriteString("\t\t" + msg.oneofFieldGo + ": &" + wrapper.wrapperType + "{\n")
			buf.WriteString("\t\t\t" + wrapper.fieldName + ": p,\n")
			buf.WriteString("\t\t},\n")
			buf.WriteString("\t}\n}\n\n")

			buf.WriteString("func (p " + wrapper.fieldType + ") ToOneof() " + msg.interfaceName + " {\n")
			buf.WriteString("\treturn p\n")
			buf.WriteString("}\n\n")
		}
	}

	return format.Source(buf.Bytes())
}

func renderOneofCodeTS(messages map[string]messageOneofInfo, wrappers map[string][]wrapperInfo) ([]byte, error) {
	var messageNames []string
	for name := range messages {
		if len(wrappers[name]) == 0 {
			continue
		}
		messageNames = append(messageNames, name)
	}
	sort.Strings(messageNames)
	if len(messageNames) == 0 {
		return nil, nil
	}

	var buf bytes.Buffer
	buf.WriteString("// Code generated by entity/gen/main.go. DO NOT EDIT.\n\n")

	// Add imports
	buf.WriteString("import {\n")
	for _, name := range messageNames {
		buf.WriteString("\t" + name + ",\n")
		for _, wrapper := range wrappers[name] {
			// wrapper.fieldType is like "*JobArchiveWaitForTapeParam" in Go
			tsType := strings.TrimPrefix(wrapper.fieldType, "*")
			buf.WriteString("\t" + tsType + ",\n")
		}
	}
	buf.WriteString("} from \".\";\n\n")

	for _, name := range messageNames {
		msg := messages[name]
		wrappersForMsg := wrappers[name]

		// TS field name for oneof is typically the Go name in camelCase
		// msg.oneofFieldGo might be "Param", "Query", "Reply". We camelCase it.
		oneofFieldTS := strings.ToLower(msg.oneofFieldGo[:1]) + msg.oneofFieldGo[1:]

		// Unpack function
		var retTypes []string
		for _, wrapper := range wrappersForMsg {
			retTypes = append(retTypes, strings.TrimPrefix(wrapper.fieldType, "*"))
		}
		retTypesStr := strings.Join(retTypes, " | ") + " | undefined"

		buf.WriteString("export function unpack" + name + "(wrapper: " + name + "): " + retTypesStr + " {\n")
		buf.WriteString("\tif (!wrapper." + oneofFieldTS + ") return undefined;\n")
		buf.WriteString("\tswitch (wrapper." + oneofFieldTS + ".oneofKind) {\n")

		for _, wrapper := range wrappersForMsg {
			// the TS oneofKind is the camelCase of the wrapper fieldName (e.g. "waitForTape" for "WaitForTape")
			tsKind := strings.ToLower(wrapper.fieldName[:1]) + wrapper.fieldName[1:]

			buf.WriteString("\t\tcase \"" + tsKind + "\":\n")
			buf.WriteString("\t\t\treturn wrapper." + oneofFieldTS + "." + tsKind + ";\n")
		}
		buf.WriteString("\t\tdefault:\n\t\t\treturn undefined;\n")
		buf.WriteString("\t}\n}\n\n")

		// Pack functions
		for _, wrapper := range wrappersForMsg {
			tsKind := strings.ToLower(wrapper.fieldName[:1]) + wrapper.fieldName[1:]
			tsType := strings.TrimPrefix(wrapper.fieldType, "*")

			buf.WriteString("export function pack" + tsType + "(param: " + tsType + "): " + name + " {\n")
			buf.WriteString("\treturn " + name + ".create({\n")
			buf.WriteString("\t\t" + oneofFieldTS + ": {\n")
			buf.WriteString("\t\t\toneofKind: \"" + tsKind + "\",\n")
			buf.WriteString("\t\t\t" + tsKind + ": param,\n")
			buf.WriteString("\t\t}\n")
			buf.WriteString("\t});\n}\n\n")
		}
	}

	// Keep generated files compatible with Git whitespace checks.
	output := bytes.TrimRight(buf.Bytes(), "\n")
	return append(output, '\n'), nil
}
