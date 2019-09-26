package upgrade

import (
	"github.com/jenkins-x/jx/pkg/cmd/opts"
	"github.com/jenkins-x/jx/pkg/config"
	"github.com/jenkins-x/jx/pkg/gits"
	"github.com/jenkins-x/jx/pkg/util"
	"github.com/stretchr/testify/require"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sigs.k8s.io/yaml"
	"testing"

	"github.com/stretchr/testify/assert"
)

type TestUpgradeBootOptions struct {
	UpgradeBootOptions
	Dir string
}

func (o *TestUpgradeBootOptions) setup() {
	dir := "test_data/upgrade_boot"

	o.UpgradeBootOptions = UpgradeBootOptions{
		CommonOptions: &opts.CommonOptions{},
		Dir:           dir,
	}
}

func TestDetermineBootConfigURL(t *testing.T) {
	t.Parallel()

	o := TestUpgradeBootOptions{}
	o.setup()

	vs, err := o.determineVersionStreamConfig()
	require.NoError(t, err, "could not get requirements version stream")

	URL, err := o.determineBootConfigURL(vs.URL)
	require.NoError(t, err, "could not determine boot config URL")
	assert.Equal(t, config.DefaultBootRepository, URL, "DetermineBootConfigURL")
}

func TestDetermineBootConfigURLNonDefault(t *testing.T) {
	t.Parallel()

	o := TestUpgradeBootOptions{}
	o.setup()

	o.GitURL = "https://github.com/my-boot-config.git"

	vs, err := o.determineVersionStreamConfig()
	require.NoError(t, err, "could not get requirements version stream")

	URL, err := o.determineBootConfigURL(vs.URL)
	require.NoError(t, err, "could not determine boot config URL")
	assert.Equal(t, o.GitURL, URL, "DetermineBootConfigURL")
}

func TestDetermineRequirementsVersionStreamConfig(t *testing.T) {
	t.Parallel()

	o := TestUpgradeBootOptions{}
	o.setup()

	vs, err := o.determineVersionStreamConfig()
	require.NoError(t, err, "could not get requirements version stream")

	assert.Equal(t, "2367726d02b8c", vs.Ref, "DetermineVersionStreamConfig Ref")
	assert.Equal(t, "https://github.com/jenkins-x/jenkins-x-versions.git", vs.URL, "DetermineVersionStreamConfig URL")
}

func TestSuppliedVersionStreamConfig(t *testing.T) {
	t.Parallel()

	o := TestUpgradeBootOptions{}
	o.setup()

	o.VersionStreamRef = "333333333"
	o.VersionStreamURL = "https://github.com/my-version-stream.git"

	vs, err := o.determineVersionStreamConfig()
	require.NoError(t, err, "could not get requirements version stream")

	assert.Equal(t, o.VersionStreamRef, vs.Ref, "DetermineVersionStreamConfig Ref")
	assert.Equal(t, o.VersionStreamURL, vs.URL, "DetermineVersionStreamConfig URL")
}

func TestLoadRequirementsConfig(t *testing.T) {
	t.Parallel()

	o := TestUpgradeBootOptions{}
	o.setup()

	reqs, _, err := o.loadRequirementsConfig()
	require.NoError(t, err, "could not get requirements config")

	requirementsFile, err := os.Open(filepath.Join(o.UpgradeBootOptions.Dir, "jx-requirements.yml"))
	require.NoError(t, err, "failed to open test requirements file")
	data, _ := ioutil.ReadAll(requirementsFile)
	var testReqs config.RequirementsConfig
	err = yaml.Unmarshal(data, &testReqs)
	require.NoError(t, err, "failed to unmarshal test requirements file")

	assert.Equal(t, testReqs.Cluster.ProjectID, reqs.Cluster.ProjectID, "LoadRequirementsConfig ProjectID")
	assert.Equal(t, testReqs.VersionStream.Ref, reqs.VersionStream.Ref, "LoadRequirementsConfig Ref")
}

func TestUpdateVersionStreamRef(t *testing.T) {
	t.Parallel()

	o := TestUpgradeBootOptions{}
	o.setup()

	tmpDir := o.createTmpRequirements(t)
	defer func() {
		err := os.RemoveAll(tmpDir)
		require.NoError(t, err, "could not clean up temp jx-requirements")
	}()

	o.UpgradeBootOptions.Dir = tmpDir
	o.SetGit(gits.NewGitFake())
	err := o.updateVersionStreamRef("22222222")
	require.NoError(t, err, "could not update version stream ref")

	vs, err := o.determineVersionStreamConfig()
	require.NoError(t, err, "could not get requirements version stream")
	assert.Equal(t, "22222222", vs.Ref, "UpdateVersionStreamRef Ref")
}

func (o *TestUpgradeBootOptions) createTmpRequirements(t *testing.T) string {
	from, err := os.Open(filepath.Join(o.UpgradeBootOptions.Dir, "jx-requirements.yml"))
	require.NoError(t, err, "unable to open test jx-requirements")

	tmpDir, err := ioutil.TempDir("", "")
	err = os.MkdirAll(tmpDir, util.DefaultWritePermissions)
	to, err := os.Create(filepath.Join(tmpDir, "jx-requirements.yml"))
	require.NoError(t, err, "unable to create tmp jx-requirements")

	_, err = io.Copy(to, from)
	require.NoError(t, err, "unable to copy test jx-requirements to tmp")
	return tmpDir
}
