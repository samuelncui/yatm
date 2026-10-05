//go:build ignore

// Check the naming of the current v1 protobuf sources before generation.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

const (
	apiPackage = "yatm.v1"
	goPackage  = "github.com/samuelncui/yatm/entity;entity"
)

var (
	fileNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*\.proto$`)
	typeNamePattern = regexp.MustCompile(`^[A-Z][a-z0-9]*(?:[A-Z][a-z0-9]*)*$`)
	fieldPattern    = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	oldInstantUnits = regexp.MustCompile(`(?:_at|^mtime|^mod_time|^write_time)_(?:ms|seconds)$`)
	genericMethods  = map[string]bool{
		"Create": true, "Get": true, "List": true, "Update": true, "Delete": true,
		"Cancel": true, "Estimate": true, "Inspect": true, "Search": true,
		"Trim": true, "Move": true, "Remove": true, "Mkdir": true, "Measure": true,
	}
	ambiguousNumericNames = map[string]bool{
		"size": true, "count": true, "speed": true, "rate": true,
		"duration": true, "timeout": true,
	}
	singularRepeatedNames = map[string]bool{
		"id": true, "file": true, "entry": true, "item": true, "location": true,
		"version": true, "path": true, "tag": true, "device": true, "position": true,
		"copy": true, "job": true, "result": true, "root": true, "candidate": true,
		"generator": true, "extension": true,
	}
)

type checker struct {
	issues    []string
	usedTypes map[string]string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// Compile the real sources so imports and type references are resolved before inspection.
	paths, err := filepath.Glob("entity/*.proto")
	if err != nil {
		return fmt.Errorf("find IDL sources failed: %w", err)
	}
	if len(paths) == 0 {
		return fmt.Errorf("no IDL sources found under entity/; run from the repository root")
	}
	tmp, err := os.CreateTemp("", "yatm-idl-*.pb")
	if err != nil {
		return fmt.Errorf("create descriptor file failed: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close descriptor file failed: %w", err)
	}
	args := []string{"-Ientity", "--descriptor_set_out=" + tmp.Name()}
	args = append(args, paths...)
	if output, err := exec.Command("protoc", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("compile IDL descriptors failed: %w\n%s", err, output)
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return fmt.Errorf("read IDL descriptors failed: %w", err)
	}
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &set); err != nil {
		return fmt.Errorf("decode IDL descriptors failed: %w", err)
	}

	// Check the public package and collect RPC-owned envelope names across all source files.
	c := checker{usedTypes: make(map[string]string)}
	for _, file := range set.File {
		c.checkFile(file)
	}
	for _, file := range set.File {
		for _, message := range file.GetMessageType() {
			c.checkMessage(file.GetName(), message)
		}
		for _, enum := range file.GetEnumType() {
			c.checkEnum(file.GetName(), enum)
		}
	}
	if len(c.issues) > 0 {
		slices.Sort(c.issues)
		return fmt.Errorf("IDL naming check failed:\n  %s", strings.Join(c.issues, "\n  "))
	}
	fmt.Printf("IDL naming check passed (%d protobuf files)\n", len(set.File))
	return nil
}

func (c *checker) checkFile(file *descriptorpb.FileDescriptorProto) {
	name := file.GetName()
	if !fileNamePattern.MatchString(filepath.Base(name)) {
		c.add(name, "filename must use lower_snake_case.proto")
	}
	if file.GetPackage() != apiPackage {
		c.add(name, "package must be %s", apiPackage)
	}
	if file.GetOptions().GetGoPackage() != goPackage {
		c.add(name, "go_package must be %q", goPackage)
	}
	if len(file.GetService()) > 1 {
		c.add(name, "declare at most one service per file")
	}
	for _, service := range file.GetService() {
		c.checkService(file, service)
	}
}

func (c *checker) checkService(file *descriptorpb.FileDescriptorProto, service *descriptorpb.ServiceDescriptorProto) {
	name := service.GetName()
	if !typeNamePattern.MatchString(name) || !strings.HasSuffix(name, "Service") {
		c.add(file.GetName(), "service %q must be a PascalCase noun ending in Service", name)
	}
	resource := strings.TrimSuffix(name, "Service")
	messages := make(map[string]bool, len(file.GetMessageType()))
	for _, message := range file.GetMessageType() {
		messages[message.GetName()] = true
	}
	for _, method := range service.GetMethod() {
		c.checkMethod(file.GetName(), name, resource, messages, method)
	}
}

func (c *checker) checkMethod(file, service, resource string, messages map[string]bool, method *descriptorpb.MethodDescriptorProto) {
	name := method.GetName()
	owner := service + "." + name
	if !typeNamePattern.MatchString(name) {
		c.add(file, "%s method must use PascalCase", owner)
	}
	if strings.HasSuffix(name, resource) {
		c.add(file, "%s repeats its service resource", owner)
	}
	request := strings.TrimPrefix(method.GetInputType(), "."+apiPackage+".")
	response := strings.TrimPrefix(method.GetOutputType(), "."+apiPackage+".")
	if !messages[request] {
		c.add(file, "%s request %q must be defined beside its service", owner, request)
	}
	if !messages[response] {
		c.add(file, "%s response %q must be defined beside its service", owner, response)
	}
	if !strings.HasSuffix(request, "Request") || !strings.HasSuffix(response, "Response") {
		c.add(file, "%s needs Request and Response envelopes", owner)
		return
	}
	requestStem := strings.TrimSuffix(request, "Request")
	responseStem := strings.TrimSuffix(response, "Response")
	if requestStem != responseStem {
		c.add(file, "%s request and response must share one operation stem", owner)
	}
	if genericMethods[name] && !strings.Contains(requestStem, resource) &&
		!strings.Contains(requestStem, strings.TrimSuffix(resource, "s")) {
		c.add(file, "%s envelope stem must include %s", owner, resource)
	}
	if verb := leadingWord(name); !strings.HasPrefix(requestStem, verb) {
		c.add(file, "%s envelope stem must start with %s", owner, verb)
	}
	for _, typ := range []string{request, response} {
		if previous, exists := c.usedTypes[typ]; exists {
			c.add(file, "%s reuses %s from %s", owner, typ, previous)
		} else {
			c.usedTypes[typ] = owner
		}
	}
}

func (c *checker) checkMessage(file string, message *descriptorpb.DescriptorProto) {
	name := message.GetName()
	if !typeNamePattern.MatchString(name) {
		c.add(file, "message %q must use PascalCase with ordinary-word initialisms", name)
	}
	if strings.HasSuffix(name, "Reply") || strings.HasSuffix(name, "Req") || strings.HasSuffix(name, "Resp") {
		c.add(file, "message %q uses a forbidden transport suffix", name)
	}
	if (strings.HasSuffix(name, "Request") || strings.HasSuffix(name, "Response")) && c.usedTypes[name] == "" {
		c.add(file, "message %q has an RPC envelope suffix but belongs to no RPC", name)
	}
	// Validate field spelling and units; optional fields also identify synthetic oneofs.
	syntheticOneofs := make(map[int32]bool)
	for _, field := range message.GetField() {
		if !fieldPattern.MatchString(field.GetName()) {
			c.add(file, "%s.%s field must use lower_snake_case", name, field.GetName())
		}
		if field.GetProto3Optional() {
			syntheticOneofs[field.GetOneofIndex()] = true
		}
		if field.GetType() == descriptorpb.FieldDescriptorProto_TYPE_BOOL && strings.HasPrefix(field.GetName(), "is_") {
			c.add(file, "%s.%s boolean field must omit is_", name, field.GetName())
		}
		if isNumeric(field.GetType()) && ambiguousNumericNames[field.GetName()] {
			c.add(file, "%s.%s numeric field needs a unit or counted subject", name, field.GetName())
		}
		if oldInstantUnits.MatchString(field.GetName()) {
			c.add(file, "%s.%s instant field must use Unix nanoseconds and an _ns suffix", name, field.GetName())
		}
		if strings.HasSuffix(field.GetName(), "_ns") && field.GetType() != descriptorpb.FieldDescriptorProto_TYPE_INT64 {
			c.add(file, "%s.%s nanosecond field must use signed int64", name, field.GetName())
		}
		if field.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED && singularRepeatedNames[field.GetName()] {
			c.add(file, "%s.%s repeated field needs a plural name", name, field.GetName())
		}
	}
	for index, oneof := range message.GetOneofDecl() {
		if syntheticOneofs[int32(index)] {
			continue
		}
		if !fieldPattern.MatchString(oneof.GetName()) {
			c.add(file, "%s.%s oneof must use lower_snake_case", name, oneof.GetName())
		}
	}
	for _, nested := range message.GetNestedType() {
		c.checkMessage(file, nested)
	}
	for _, enum := range message.GetEnumType() {
		c.checkEnum(file, enum)
	}
}

func (c *checker) checkEnum(file string, enum *descriptorpb.EnumDescriptorProto) {
	name := enum.GetName()
	if !typeNamePattern.MatchString(name) {
		c.add(file, "enum %q must use PascalCase with ordinary-word initialisms", name)
	}
	prefix := upperSnake(name)
	values := enum.GetValue()
	if len(values) == 0 || values[0].GetName() != prefix+"_UNSPECIFIED" || values[0].GetNumber() != 0 {
		c.add(file, "enum %s must start with %s_UNSPECIFIED = 0", name, prefix)
	}
	for _, value := range values {
		if !strings.HasPrefix(value.GetName(), prefix+"_") {
			c.add(file, "enum %s value %q needs the %s_ prefix", name, value.GetName(), prefix)
		}
	}
}

func (c *checker) add(file, format string, args ...any) {
	c.issues = append(c.issues, file+": "+fmt.Sprintf(format, args...))
}

func leadingWord(name string) string {
	for i := 1; i < len(name); i++ {
		if name[i] >= 'A' && name[i] <= 'Z' {
			return name[:i]
		}
	}
	return name
}

func upperSnake(name string) string {
	var result strings.Builder
	for i, char := range name {
		if i > 0 && char >= 'A' && char <= 'Z' {
			result.WriteByte('_')
		}
		if char >= 'a' && char <= 'z' {
			char -= 'a' - 'A'
		}
		result.WriteRune(char)
	}
	return result.String()
}

func isNumeric(typ descriptorpb.FieldDescriptorProto_Type) bool {
	switch typ {
	case descriptorpb.FieldDescriptorProto_TYPE_DOUBLE,
		descriptorpb.FieldDescriptorProto_TYPE_FLOAT,
		descriptorpb.FieldDescriptorProto_TYPE_INT64,
		descriptorpb.FieldDescriptorProto_TYPE_UINT64,
		descriptorpb.FieldDescriptorProto_TYPE_INT32,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED64,
		descriptorpb.FieldDescriptorProto_TYPE_FIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_UINT32,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED32,
		descriptorpb.FieldDescriptorProto_TYPE_SFIXED64,
		descriptorpb.FieldDescriptorProto_TYPE_SINT32,
		descriptorpb.FieldDescriptorProto_TYPE_SINT64:
		return true
	default:
		return false
	}
}
