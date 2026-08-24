// Command proto-policy enforces Glider's public Protobuf contract rules without
// requiring a network-installed compiler. Buf remains the authoritative syntax
// and wire-compatibility validator during code generation.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	packagePattern = regexp.MustCompile(`^package\s+([a-zA-Z0-9_.]+);$`)
	rpcPattern     = regexp.MustCompile(`^rpc\s+([A-Za-z0-9_]+)\(([A-Za-z0-9_.]+)\)\s+returns\s+\(([A-Za-z0-9_.]+)\);$`)
	fieldPattern   = regexp.MustCompile(`^(?:repeated\s+)?(?:map<[^>]+>|[A-Za-z0-9_.]+)\s+[a-z][a-z0-9_]*\s*=\s*([0-9]+);`)
	enumValue      = regexp.MustCompile(`^([A-Z][A-Z0-9_]*)\s*=\s*([0-9]+);$`)
)

func main() {
	root := flag.String("root", "api/proto/glider/v2", "typed API schema directory")
	flag.Parse()
	if err := validate(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validate(root string) error {
	files, err := filepath.Glob(filepath.Join(root, "*.proto"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("typed API schema contains no .proto files")
	}
	sort.Strings(files)
	var violations []string
	rpcs := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		rel := filepath.ToSlash(path)
		if !strings.Contains(text, `syntax = "proto3";`) {
			violations = append(violations, rel+": syntax must be proto3")
		}
		if strings.Contains(text, "google.protobuf.Any") || strings.Contains(text, "google.protobuf.Value") {
			violations = append(violations, rel+": Any and Value are forbidden in the public contract")
		}
		if strings.Contains(text, "google.protobuf.Struct") {
			violations = append(violations, rel+": Struct is forbidden in the typed public contract")
		}
		violations = append(violations, validateFile(rel, text, &rpcs)...)
	}
	if rpcs == 0 {
		violations = append(violations, "typed API defines no RPCs")
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		return errors.New(strings.Join(violations, "\n"))
	}
	fmt.Printf("PROTO POLICY GREEN: %d typed schema files, %d RPCs, no untyped public fields\n", len(files), rpcs)
	return nil
}

func validateFile(path, text string, rpcCount *int) []string {
	var violations []string
	scanner := bufio.NewScanner(strings.NewReader(text))
	packageSeen := false
	stack := make([]scope, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "//", 2)[0])
		if line == "" {
			continue
		}
		if match := packagePattern.FindStringSubmatch(line); match != nil {
			packageSeen = true
			if match[1] != "glider.v2" {
				violations = append(violations, at(path, lineNumber, "package must be glider.v2"))
			}
		}
		if strings.HasPrefix(line, "message ") && strings.HasSuffix(line, "{") {
			stack = append(stack, scope{kind: "message", fields: make(map[int]int)})
			continue
		}
		if strings.HasPrefix(line, "enum ") && strings.HasSuffix(line, "{") {
			stack = append(stack, scope{kind: "enum"})
			continue
		}
		if strings.HasPrefix(line, "service ") && strings.HasSuffix(line, "{") {
			stack = append(stack, scope{kind: "service"})
			continue
		}
		if line == "}" {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if match := rpcPattern.FindStringSubmatch(line); match != nil {
			*rpcCount++
			method, request, response := match[1], match[2], match[3]
			if request != method+"Request" {
				violations = append(violations, at(path, lineNumber, "RPC request must be named "+method+"Request"))
			}
			if response != method+"Response" {
				violations = append(violations, at(path, lineNumber, "RPC response must be named "+method+"Response"))
			}
			if strings.HasPrefix(request, "google.protobuf.") || strings.HasPrefix(response, "google.protobuf.") {
				violations = append(violations, at(path, lineNumber, "RPC boundaries must use Glider-owned messages"))
			}
			continue
		}
		if len(stack) == 0 {
			continue
		}
		current := &stack[len(stack)-1]
		if current.kind == "message" {
			if match := fieldPattern.FindStringSubmatch(line); match != nil {
				number, _ := strconv.Atoi(match[1])
				if number <= 0 || number > 536870911 || (number >= 19000 && number <= 19999) {
					violations = append(violations, at(path, lineNumber, "invalid or reserved field number"))
				}
				if previous, exists := current.fields[number]; exists {
					violations = append(violations, at(path, lineNumber, fmt.Sprintf("field number %d duplicates line %d", number, previous)))
				}
				current.fields[number] = lineNumber
			}
		}
		if current.kind == "enum" {
			if match := enumValue.FindStringSubmatch(line); match != nil && !current.enumSeen {
				current.enumSeen = true
				if match[2] != "0" || !strings.HasSuffix(match[1], "_UNSPECIFIED") {
					violations = append(violations, at(path, lineNumber, "first enum value must be zero and end in _UNSPECIFIED"))
				}
			}
		}
	}
	if !packageSeen {
		violations = append(violations, path+": package declaration is required")
	}
	if len(stack) != 0 {
		violations = append(violations, path+": unbalanced declaration braces")
	}
	if err := scanner.Err(); err != nil {
		violations = append(violations, path+": "+err.Error())
	}
	return violations
}

type scope struct {
	kind     string
	fields   map[int]int
	enumSeen bool
}

func at(path string, line int, message string) string {
	return fmt.Sprintf("%s:%d: %s", path, line, message)
}
