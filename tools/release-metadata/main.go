// Command release-metadata creates and verifies deterministic release evidence.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type module struct {
	Path, Version string
	Main          bool
	Replace       *module
}

func main() {
	if len(os.Args) < 2 {
		fatal(errors.New("usage: release-metadata sbom|provenance|verify"))
	}
	var err error
	switch os.Args[1] {
	case "sbom":
		err = sbom(os.Args[2:])
	case "provenance":
		err = provenance(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		err = errors.New("unknown command")
	}
	if err != nil {
		fatal(err)
	}
}

func modules() ([]module, error) {
	command := exec.Command("go", "list", "-m", "-json", "all")
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	var result []module
	for {
		var item module
		if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func sbom(args []string) error {
	fs := flag.NewFlagSet("sbom", flag.ContinueOnError)
	output := fs.String("output", "", "SPDX JSON output")
	version := fs.String("version", "", "release version")
	commit := fs.String("commit", "", "source commit")
	epoch := fs.Int64("epoch", 0, "creation Unix timestamp")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output == "" || *version == "" || !commitPattern.MatchString(*commit) || *epoch <= 0 {
		return errors.New("--output, --version, 40-character --commit, and positive --epoch are required")
	}
	mods, err := modules()
	if err != nil {
		return err
	}
	packages := make([]map[string]any, 0, len(mods))
	relationships := make([]map[string]string, 0, len(mods))
	rootID := "SPDXRef-Package-Glider"
	for _, item := range mods {
		id := spdxID(item.Path + "@" + item.Version)
		name, moduleVersion := item.Path, item.Version
		if item.Main {
			id, name, moduleVersion = rootID, "Glider", *version
		}
		if item.Replace != nil {
			name = item.Replace.Path
			if item.Replace.Version != "" {
				moduleVersion = item.Replace.Version
			}
		}
		if moduleVersion == "" {
			moduleVersion = "NOASSERTION"
		}
		pkg := map[string]any{"SPDXID": id, "name": name, "versionInfo": moduleVersion, "downloadLocation": "NOASSERTION", "filesAnalyzed": false, "licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION", "copyrightText": "NOASSERTION"}
		if !item.Main && item.Version != "" {
			pkg["externalRefs"] = []map[string]string{{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": "pkg:golang/" + item.Path + "@" + item.Version}}
			relationships = append(relationships, map[string]string{"spdxElementId": rootID, "relationshipType": "DEPENDS_ON", "relatedSpdxElement": id})
		}
		packages = append(packages, pkg)
	}
	document := map[string]any{
		"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT",
		"name": "Glider-" + *version, "documentNamespace": "https://github.com/santinomarial/Glider/releases/" + *version + "/" + *commit,
		"creationInfo": map[string]any{"created": time.Unix(*epoch, 0).UTC().Format(time.RFC3339), "creators": []string{"Tool: glider-release-metadata"}},
		"packages":     packages, "relationships": relationships,
	}
	return writeJSON(*output, document)
}

func provenance(args []string) error {
	fs := flag.NewFlagSet("provenance", flag.ContinueOnError)
	output := fs.String("output", "", "SLSA provenance JSON output")
	version := fs.String("version", "", "release version")
	commit := fs.String("commit", "", "source commit")
	epoch := fs.Int64("epoch", 0, "build Unix timestamp")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *output == "" || *version == "" || !commitPattern.MatchString(*commit) || *epoch <= 0 || fs.NArg() == 0 {
		return errors.New("metadata flags and at least one artifact are required")
	}
	artifacts := append([]string(nil), fs.Args()...)
	sort.Strings(artifacts)
	subjects := make([]map[string]any, 0, len(artifacts))
	for _, artifact := range artifacts {
		digest, err := fileDigest(artifact)
		if err != nil {
			return err
		}
		subjects = append(subjects, map[string]any{"name": filepath.Base(artifact), "digest": map[string]string{"sha256": digest}})
	}
	stamp := time.Unix(*epoch, 0).UTC().Format(time.RFC3339)
	statement := map[string]any{
		"_type": "https://in-toto.io/Statement/v1", "subject": subjects,
		"predicateType": "https://slsa.dev/provenance/v1",
		"predicate": map[string]any{
			"buildDefinition": map[string]any{"buildType": "https://github.com/santinomarial/Glider/release/v1", "externalParameters": map[string]string{"version": *version}, "resolvedDependencies": []map[string]any{{"uri": "git+https://github.com/santinomarial/Glider@" + *commit, "digest": map[string]string{"gitCommit": *commit}}}},
			"runDetails":      map[string]any{"builder": map[string]string{"id": "https://github.com/santinomarial/Glider/scripts/release.sh"}, "metadata": map[string]any{"invocationId": *commit + ":" + *version, "startedOn": stamp, "finishedOn": stamp}},
		},
	}
	return writeJSON(*output, statement)
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	directory := fs.String("dir", "dist", "release directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var spdx struct {
		Version  string           `json:"spdxVersion"`
		Packages []map[string]any `json:"packages"`
	}
	if err := readJSON(filepath.Join(*directory, "sbom.spdx.json"), &spdx); err != nil {
		return err
	}
	if spdx.Version != "SPDX-2.3" || len(spdx.Packages) < 2 {
		return errors.New("invalid or empty SPDX SBOM")
	}
	var provenance struct {
		Type          string `json:"_type"`
		PredicateType string `json:"predicateType"`
		Subject       []struct {
			Name   string            `json:"name"`
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
	}
	if err := readJSON(filepath.Join(*directory, "provenance.intoto.json"), &provenance); err != nil {
		return err
	}
	if provenance.Type != "https://in-toto.io/Statement/v1" || provenance.PredicateType != "https://slsa.dev/provenance/v1" || len(provenance.Subject) == 0 {
		return errors.New("invalid SLSA provenance")
	}
	for _, subject := range provenance.Subject {
		if filepath.Base(subject.Name) != subject.Name || subject.Digest["sha256"] == "" {
			return errors.New("unsafe provenance subject")
		}
		digest, err := fileDigest(filepath.Join(*directory, subject.Name))
		if err != nil {
			return err
		}
		if digest != subject.Digest["sha256"] {
			return fmt.Errorf("provenance digest mismatch for %s", subject.Name)
		}
	}
	return nil
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func spdxID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "SPDXRef-Package-" + hex.EncodeToString(sum[:12])
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(value)
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	return closeErr
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "release-metadata:", err)
	os.Exit(1)
}
