package deploy_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const requiredComposePublication = `0.0.0.0:8080:8080`
const forbiddenLoopbackPublication = `127.0.0.1:8080:8080`

func TestComposePublishesServiceOnAllIPv4Interfaces(t *testing.T) {
	repositoryRoot := locateRepositoryRoot(t)
	composeContents := readRepositoryFile(t, repositoryRoot, "docker-compose.yml")

	requiredMapping := `- "` + requiredComposePublication + `"`
	if !containsActiveYAMLLine(composeContents, requiredMapping) {
		t.Fatalf(
			"docker-compose.yml must preserve the project deployment mapping %q",
			requiredMapping,
		)
	}
	forbiddenMapping := `- "` + forbiddenLoopbackPublication + `"`
	if containsActiveYAMLLine(composeContents, forbiddenMapping) {
		t.Fatalf(
			"docker-compose.yml must not regress to loopback-only publication %q",
			forbiddenMapping,
		)
	}
}

func TestDeploymentDocumentationPreservesComposePublication(t *testing.T) {
	repositoryRoot := locateRepositoryRoot(t)
	documentationPaths := []string{
		"README.md",
		"README_CN.md",
	}

	for _, documentationPath := range documentationPaths {
		documentationPath := documentationPath
		t.Run(documentationPath, func(t *testing.T) {
			documentationContents := readRepositoryFile(t, repositoryRoot, documentationPath)
			if !strings.Contains(documentationContents, requiredComposePublication) {
				t.Fatalf(
					"%s must document the project Compose publication %q",
					documentationPath,
					requiredComposePublication,
				)
			}
		})
	}
}

func TestDefaultModelRemainsConsistentAcrossDeploymentLayers(t *testing.T) {
	repositoryRoot := locateRepositoryRoot(t)
	advancedEnvironmentContents := readRepositoryFile(t, repositoryRoot, "advanced.env")
	configurationContents := readRepositoryFile(t, repositoryRoot, "internal/config/config.go")
	panelSettingsContents := readRepositoryFile(
		t,
		repositoryRoot,
		filepath.Join("internal", "panelui", "static", "js", "pages", "settings.js"),
	)

	advancedEnvironmentModel := extractRequiredMatch(
		t,
		advancedEnvironmentContents,
		`(?m)^\s*GROK_MODEL\s*=\s*([^\s#]+)\s*$`,
		"advanced.env GROK_MODEL",
	)
	applicationDefaultModel := extractRequiredMatch(
		t,
		configurationContents,
		`(?m)^\s*defaultModel\s*=\s*"([^"]+)"\s*$`,
		"internal/config defaultModel",
	)

	if applicationDefaultModel != advancedEnvironmentModel {
		t.Fatalf(
			"application default model %q must match advanced.env GROK_MODEL %q",
			applicationDefaultModel,
			advancedEnvironmentModel,
		)
	}

	expectedDocumentationRow := "| `GROK_MODEL` | `" + advancedEnvironmentModel + "` |"
	for _, documentationPath := range []string{"README.md", "README_CN.md"} {
		documentationContents := readRepositoryFile(t, repositoryRoot, documentationPath)
		if !strings.Contains(documentationContents, expectedDocumentationRow) {
			t.Fatalf(
				"%s must document GROK_MODEL default %q from advanced.env",
				documentationPath,
				advancedEnvironmentModel,
			)
		}
	}

	expectedPanelPlaceholder := `placeholder="` + advancedEnvironmentModel + `"`
	if !strings.Contains(panelSettingsContents, expectedPanelPlaceholder) {
		t.Fatalf(
			"panel model placeholder must match advanced.env GROK_MODEL %q",
			advancedEnvironmentModel,
		)
	}
}

func TestDeploymentContractWorkflowRunsForRepositoryChanges(t *testing.T) {
	repositoryRoot := locateRepositoryRoot(t)
	workflowContents := readRepositoryFile(
		t,
		repositoryRoot,
		filepath.Join(".github", "workflows", "deployment-contract.yml"),
	)

	requiredWorkflowLines := []string{
		"push:",
		"pull_request:",
		"run: go test ./test/deploy",
	}
	for _, requiredWorkflowLine := range requiredWorkflowLines {
		if !containsActiveYAMLLine(workflowContents, requiredWorkflowLine) {
			t.Fatalf(
				"deployment contract workflow must contain active line %q",
				requiredWorkflowLine,
			)
		}
	}
}

func TestDockerReleasePublishesVersionAndLatestTags(t *testing.T) {
	repositoryRoot := locateRepositoryRoot(t)
	workflowContents := readRepositoryFile(
		t,
		repositoryRoot,
		filepath.Join(".github", "workflows", "docker-publish.yml"),
	)

	requiredTagLines := []string{
		`type=raw,value=${{ env.RELEASE_TAG }}`,
		"type=raw,value=latest",
	}
	for _, requiredTagLine := range requiredTagLines {
		if !containsActiveYAMLLine(workflowContents, requiredTagLine) {
			t.Fatalf("Docker release workflow must publish tag line %q", requiredTagLine)
		}
	}
}

func TestContainsActiveYAMLLineIgnoresCommentedMappings(t *testing.T) {
	composeContents := strings.Join([]string{
		"ports:",
		`  # - "0.0.0.0:8080:8080"`,
		`  - "127.0.0.1:8080:8080"`,
	}, "\n")

	if containsActiveYAMLLine(composeContents, `- "0.0.0.0:8080:8080"`) {
		t.Fatal("commented all-interface mapping must not satisfy the deployment contract")
	}
	if !containsActiveYAMLLine(composeContents, `- "127.0.0.1:8080:8080"`) {
		t.Fatal("active loopback mapping must remain detectable")
	}
}

func locateRepositoryRoot(t *testing.T) string {
	t.Helper()

	packageDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("determine deployment test working directory: %v", err)
	}

	return filepath.Clean(filepath.Join(packageDirectory, "..", ".."))
}

func readRepositoryFile(t *testing.T, repositoryRoot string, relativePath string) string {
	t.Helper()

	absolutePath := filepath.Join(repositoryRoot, relativePath)
	fileContents, err := os.ReadFile(absolutePath)
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}

	return string(fileContents)
}

func containsActiveYAMLLine(yamlContents string, expectedLine string) bool {
	for yamlLine := range strings.SplitSeq(yamlContents, "\n") {
		if strings.TrimSpace(yamlLine) == expectedLine {
			return true
		}
	}

	return false
}

func extractRequiredMatch(t *testing.T, contents string, pattern string, description string) string {
	t.Helper()

	compiledPattern := regexp.MustCompile(pattern)
	matches := compiledPattern.FindStringSubmatch(contents)
	if len(matches) != 2 {
		t.Fatalf("could not find %s", description)
	}

	return matches[1]
}
