package main

import (
	"fmt"
	clientTestUtils "github.com/jfrog/jfrog-client-go/utils/tests"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	buildinfo "github.com/jfrog/build-info-go/entities"

	gofrogcmd "github.com/jfrog/gofrog/io"
	"github.com/jfrog/jfrog-cli-core/v2/utils/coreutils"
	"github.com/jfrog/jfrog-cli/inttestutils"
	"github.com/jfrog/jfrog-cli/utils/tests"
	"github.com/jfrog/jfrog-client-go/utils/io/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type PipCmd struct {
	Command string
	Options []string
}

func TestPipInstallNativeSyntax(t *testing.T) {
	testPipInstall(t, false)
}

// Deprecated
func TestPipInstallLegacy(t *testing.T) {
	testPipInstall(t, true)
}

func testPipInstall(t *testing.T, isLegacy bool) {
	// Init pip.
	initPipTest(t)

	// Add virtual-environment path to 'PATH' for executing all pip and python commands inside the virtual-environment.
	pathValue := setPathEnvForPipInstall(t)
	if t.Failed() {
		t.FailNow()
	}
	defer clientTestUtils.SetEnvAndAssert(t, "PATH", pathValue)

	// Check pip env is clean.
	validateEmptyPipEnv(t)

	// Populate cli config with 'default' server.
	oldHomeDir, newHomeDir := prepareHomeDir(t)
	defer func() {
		clientTestUtils.SetEnvAndAssert(t, coreutils.HomeDir, oldHomeDir)
		clientTestUtils.RemoveAllAndAssert(t, newHomeDir)
	}()

	// Create test cases.
	allTests := []struct {
		name                 string
		project              string
		outputFolder         string
		moduleId             string
		args                 []string
		expectedDependencies int
		cleanAfterExecution  bool
	}{
		{"setuppy", "setuppyproject", "setuppy", "jfrog-python-example", []string{".", "--no-cache-dir", "--force-reinstall", "--build-name=" + tests.PipBuildName}, 3, true},
		{"setuppy-verbose", "setuppyproject", "setuppy-verbose", "jfrog-python-example", []string{".", "--no-cache-dir", "--force-reinstall", "-v", "--build-name=" + tests.PipBuildName}, 3, true},
		{"setuppy-with-module", "setuppyproject", "setuppy-with-module", "setuppy-with-module", []string{".", "--no-cache-dir", "--force-reinstall", "--build-name=" + tests.PipBuildName, "--module=setuppy-with-module"}, 3, true},
		{"requirements", "requirementsproject", "requirements", tests.PipBuildName, []string{"-r", "requirements.txt", "--no-cache-dir", "--force-reinstall", "--build-name=" + tests.PipBuildName}, 5, true},
		{"requirements-verbose", "requirementsproject", "requirements-verbose", tests.PipBuildName, []string{"-r", "requirements.txt", "--no-cache-dir", "--force-reinstall", "-v", "--build-name=" + tests.PipBuildName}, 5, false},
		{"requirements-use-cache", "requirementsproject", "requirements-verbose", "requirements-verbose-use-cache", []string{"-r", "requirements.txt", "--module=requirements-verbose-use-cache", "--build-name=" + tests.PipBuildName}, 5, true},
	}

	// Run test cases.
	for buildNumber, test := range allTests {
		t.Run(test.name, func(t *testing.T) {
			if isLegacy {
				test.args = append([]string{"rt", "pip-install"}, test.args...)
			} else {
				test.args = append([]string{"pip", "install"}, test.args...)
			}
			testPipCmd(t, test.name, createPipProject(t, test.outputFolder, test.project), strconv.Itoa(buildNumber), test.moduleId, test.expectedDependencies, test.args)
			if test.cleanAfterExecution {
				// cleanup
				inttestutils.DeleteBuild(serverDetails.ArtifactoryUrl, tests.PipBuildName, artHttpDetails)
				cleanPipTest(t, test.name)
			}
		})
	}
	cleanPipTest(t, "cleanup")
	tests.CleanFileSystem()
}

func testPipCmd(t *testing.T, outputFolder, projectPath, buildNumber, module string, expectedDependencies int, args []string) {
	wd, err := os.Getwd()
	assert.NoError(t, err, "Failed to get current dir")
	chdirCallback := clientTestUtils.ChangeDirWithCallback(t, wd, projectPath)
	defer chdirCallback()

	args = append(args, "--build-number="+buildNumber)

	jfrogCli := tests.NewJfrogCli(execMain, "jfrog", "")
	err = jfrogCli.Exec(args...)
	if err != nil {
		assert.Fail(t, "Failed executing pip install command", err.Error())
		cleanPipTest(t, outputFolder)
		return
	}

	inttestutils.ValidateGeneratedBuildInfoModule(t, tests.PipBuildName, buildNumber, "", []string{module}, buildinfo.Python)
	assert.NoError(t, artifactoryCli.Exec("bp", tests.PipBuildName, buildNumber))

	publishedBuildInfo, found, err := tests.GetBuildInfo(serverDetails, tests.PipBuildName, buildNumber)
	if err != nil {
		assert.NoError(t, err)
		return
	}
	if !found {
		assert.True(t, found, "build info was expected to be found")
		return
	}
	buildInfo := publishedBuildInfo.BuildInfo
	require.NotEmpty(t, buildInfo.Modules, "Pip build info was not generated correctly, no modules were created.")
	assert.Len(t, buildInfo.Modules[0].Dependencies, expectedDependencies, "Incorrect number of artifacts found in the build-info")
	assert.Equal(t, module, buildInfo.Modules[0].Id, "Unexpected module name")
}

func cleanPipTest(t *testing.T, outFolder string) {
	// Clean pip environment from installed packages.
	pipFreezeCmd := &PipCmd{Command: "freeze", Options: []string{"--local"}}
	out, err := gofrogcmd.RunCmdOutput(pipFreezeCmd)
	if err != nil {
		t.Fatal(err)
	}

	// If no packages to uninstall, return.
	if out == "" {
		return
	}

	// Save freeze output to file.
	freezeTarget, err := fileutils.CreateFilePath(tests.Temp, outFolder+"-freeze.txt")
	assert.NoError(t, err)
	file, err := os.Create(freezeTarget)
	assert.NoError(t, err)
	defer func() {
		assert.NoError(t, file.Close())
	}()
	_, err = file.Write([]byte(out))
	assert.NoError(t, err)

	// Delete freezed packages.
	pipUninstallCmd := &PipCmd{Command: "uninstall", Options: []string{"-y", "-r", freezeTarget}}
	err = gofrogcmd.RunCmd(pipUninstallCmd)
	if err != nil {
		t.Fatal(err)
	}
}

func createPipProject(t *testing.T, outFolder, projectName string) string {
	projectSrc := filepath.Join(filepath.FromSlash(tests.GetTestResourcesPath()), "pip", projectName)
	projectTarget := filepath.Join(tests.Out, outFolder+"-"+projectName)
	err := fileutils.CreateDirIfNotExist(projectTarget)
	assert.NoError(t, err)

	// Copy pip-installation file.
	err = fileutils.CopyDir(projectSrc, projectTarget, true, nil)
	assert.NoError(t, err)

	// Copy pip-config file.
	configSrc := filepath.Join(filepath.FromSlash(tests.GetTestResourcesPath()), "pip", "pip.yaml")
	configTarget := filepath.Join(projectTarget, ".jfrog", "projects")
	_, err = tests.ReplaceTemplateVariables(configSrc, configTarget)
	assert.NoError(t, err)
	return projectTarget
}

func initPipTest(t *testing.T) {
	if !*tests.TestPip {
		t.Skip("Skipping Pip test. To run Pip test add the '-test.pip=true' option.")
	}
	require.True(t, isRepoExist(tests.PypiRemoteRepo), "Pypi test remote repository doesn't exist.")
	require.True(t, isRepoExist(tests.PypiVirtualRepo), "Pypi test virtual repository doesn't exist.")
}

func setPathEnvForPipInstall(t *testing.T) string {
	// Keep original value of 'PATH'.
	pathValue, exists := os.LookupEnv("PATH")
	if !exists {
		t.Fatal("Couldn't find PATH variable, failing pip tests.")
	}

	// Append the path.
	virtualEnvPath := *tests.PipVirtualEnv
	if virtualEnvPath != "" {
		var newPathValue string
		if coreutils.IsWindows() {
			newPathValue = fmt.Sprintf("%s;%s", virtualEnvPath, pathValue)
		} else {
			newPathValue = fmt.Sprintf("%s:%s", virtualEnvPath, pathValue)
		}
		err := os.Setenv("PATH", newPathValue)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Return original PATH value.
	return pathValue
}

// Ensure that the provided pip virtual-environment is empty from installed packages.
func validateEmptyPipEnv(t *testing.T) {
	//pipFreezeCmd := &PipFreezeCmd{Executable: "pip", Command: "freeze"}
	pipFreezeCmd := &PipCmd{Command: "freeze", Options: []string{"--local"}}
	out, err := gofrogcmd.RunCmdOutput(pipFreezeCmd)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("Provided pip virtual-environment contains installed packages: %s\n. Please provide a clean environment.", out)
	}
}

func (pfc *PipCmd) GetCmd() *exec.Cmd {
	var cmd []string
	cmd = append(cmd, "pip")
	cmd = append(cmd, pfc.Command)
	cmd = append(cmd, pfc.Options...)
	return exec.Command(cmd[0], cmd[1:]...)
}

func (pfc *PipCmd) GetEnv() map[string]string {
	return map[string]string{}
}

func (pfc *PipCmd) GetStdWriter() io.WriteCloser {
	return nil
}

func (pfc *PipCmd) GetErrWriter() io.WriteCloser {
	return nil
}
