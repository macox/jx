package upgrade

import (
	"fmt"
	"github.com/jenkins-x/jx/pkg/auth"
	"github.com/jenkins-x/jx/pkg/boot"
	"github.com/jenkins-x/jx/pkg/cmd/helper"
	"github.com/jenkins-x/jx/pkg/cmd/opts"
	"github.com/jenkins-x/jx/pkg/cmd/templates"
	"github.com/jenkins-x/jx/pkg/config"
	"github.com/jenkins-x/jx/pkg/gits"
	"github.com/jenkins-x/jx/pkg/kube"
	"github.com/jenkins-x/jx/pkg/log"
	"github.com/jenkins-x/jx/pkg/util"
	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
	"github.com/spf13/cobra"
	"io/ioutil"
	"os"
	"strings"
)

// UpgradeBootOptions options for the command
type UpgradeBootOptions struct {
	*opts.CommonOptions
	GitURL           string
	VersionStreamURL string
	VersionStreamRef string
	Dir              string
}

var (
	upgradeBootLong = templates.LongDesc(`
		This command creates a pr for upgrading a jx boot gitOps cluster, incorporating changes to the boot
        config and version stream ref
`)

	upgradeBootExample = templates.Examples(`
		# create pr for upgrading a jx boot gitOps cluster
		jx upgrade boot
`)
)

// NewCmdUpgradeBoot creates the command
func NewCmdUpgradeBoot(commonOpts *opts.CommonOptions) *cobra.Command {
	options := &UpgradeBootOptions{
		CommonOptions: commonOpts,
	}
	cmd := &cobra.Command{
		Use:     "boot",
		Short:   "Upgrades jx boot config",
		Long:    upgradeBootLong,
		Example: upgradeBootExample,
		Run: func(cmd *cobra.Command, args []string) {
			options.Cmd = cmd
			options.Args = args
			err := options.Run()
			helper.CheckErr(err)
		},
	}
	cmd.Flags().StringVarP(&options.GitURL, "git-url", "u", "", "override the Git clone URL for the JX Boot source to start from, ignoring the versions stream. Normally specified with git-ref as well")
	cmd.Flags().StringVarP(&options.VersionStreamURL, "versions-repo", "", "", "the bootstrap URL for the versions repo. Once the boot config is cloned, the repo will be then read from the jx-requirements.yaml")
	cmd.Flags().StringVarP(&options.VersionStreamRef, "versions-ref", "", "", "the bootstrap ref for the versions repo. Once the boot config is cloned, the repo will be then read from the jx-requirements.yaml")
	cmd.Flags().StringVarP(&options.Dir, "dir", "d", "", "the directory to look for the Jenkins X Pipeline and requirements")
	return cmd
}

// Run runs this command
func (o *UpgradeBootOptions) Run() error {
	err := o.setupGitConfig(o.Dir)
	if err != nil {
		return errors.Wrap(err, "failed to setup git config")
	}

	if o.Dir == "" {
		err := o.cloneDevEnv()
		if err != nil {
			return errors.Wrap(err, "failed to clone dev environment repo")
		}
	}

	reqsVersionStream, err := o.determineVersionStreamConfig()
	if err != nil {
		return errors.Wrap(err, "failed to get requirements version stream")
	}

	upgradeVersionSha, err := o.upgradeAvailable(reqsVersionStream.URL, reqsVersionStream.Ref, "master")
	if err != nil {
		return errors.Wrap(err, "failed to get check for available update")
	}
	if upgradeVersionSha == "" {
		return nil
	}

	localBranch, err := o.checkoutNewBranch()
	if err != nil {
		return errors.Wrap(err, "failed to checkout upgrade_branch")
	}

	bootConfigURL, err := o.determineBootConfigURL(reqsVersionStream.URL)
	if err != nil {
		return errors.Wrap(err, "failed to determine boot configuration URL")
	}

	err = o.updateBootConfig(reqsVersionStream.URL, reqsVersionStream.Ref, bootConfigURL, upgradeVersionSha)
	if err != nil {
		return errors.Wrap(err, "failed to update boot configuration")
	}

	err = o.updateVersionStreamRef(upgradeVersionSha)
	if err != nil {
		return errors.Wrap(err, "failed to update version stream ref")
	}

	err = o.raisePR()
	if err != nil {
		return errors.Wrap(err, "failed to raise pr")
	}

	err = o.deleteLocalBranch(localBranch)
	if err != nil {
		return errors.Wrapf(err, "failed to delete local branch %s", localBranch)
	}
	return nil
}

func (o *UpgradeBootOptions) determineBootConfigURL(versionStreamURL string) (string, error) {
	if o.GitURL == "" {
		var bootConfigURL string
		if versionStreamURL == config.DefaultVersionsURL {
			bootConfigURL = config.DefaultBootRepository
		}
		if versionStreamURL == config.DefaultCloudBeesVersionsURL {
			bootConfigURL = config.DefaultCloudBeesBootRepository
		}

		if bootConfigURL == "" {
			return "", fmt.Errorf("unable to determine default boot config URL")
		}
		log.Logger().Infof("using default boot config %s", bootConfigURL)
		return bootConfigURL, nil
	}
	return o.GitURL, nil
}

func (o *UpgradeBootOptions) determineVersionStreamConfig() (*config.VersionStreamConfig, error) {
	versionStreamConfig := config.VersionStreamConfig{}

	if o.VersionStreamURL == "" && o.VersionStreamRef == "" {
		requirements, _, err := o.loadRequirementsConfig()
		if err != nil {
			return nil, errors.Wrap(err, "failed to load requirements config")
		}
		versionStreamConfig = requirements.VersionStream
	} else {
		versionStreamConfig.Ref = o.VersionStreamRef
		versionStreamConfig.URL = o.VersionStreamURL
	}

	if versionStreamConfig.URL == "" || versionStreamConfig.Ref == "" {
		log.Logger().Warnf("Incomplete version stream reference %s @ %s", versionStreamConfig.URL, versionStreamConfig.Ref)
		versionStreamConfig = defaultVersionStreamConfig()
	}
	return &versionStreamConfig, nil
}

func defaultVersionStreamConfig() config.VersionStreamConfig {
	versionStreamConfig := config.VersionStreamConfig{}
	if config.LoadActiveInstallProfile() == config.CloudBeesProfile {
		versionStreamConfig.Ref = config.DefaultCloudBeesVersionsRef
		versionStreamConfig.URL = config.DefaultVersionsURL
	} else {
		versionStreamConfig.Ref = config.DefaultVersionsRef
		versionStreamConfig.URL = config.DefaultVersionsURL
	}
	return versionStreamConfig
}

func (o *UpgradeBootOptions) loadRequirementsConfig() (*config.RequirementsConfig, string, error) {
	requirements, requirementsFile, err := config.LoadRequirementsConfig(o.Dir)
	if err != nil {
		return nil, "", errors.Wrapf(err, "failed to load requirements config from %s", o.Dir)
	}
	exists, err := util.FileExists(requirementsFile)
	if err != nil {
		return nil, "", errors.Wrapf(err, "failed to check if file %s exists", requirementsFile)
	}
	if !exists {
		return nil, "", fmt.Errorf("no requirements file %s ensure you are running this command inside a GitOps clone", requirementsFile)
	}
	return requirements, requirementsFile, nil
}

func (o *UpgradeBootOptions) upgradeAvailable(versionStreamURL string, versionStreamRef string, upgradeRef string) (string, error) {
	versionsDir, err := o.CloneJXVersionsRepo(versionStreamURL, upgradeRef)
	if err != nil {
		return "", errors.Wrapf(err, "failed to clone versions repo %s", versionStreamURL)
	}
	upgradeVersionSha, err := o.Git().GetCommitPointedToByTag(versionsDir, upgradeRef)
	if err != nil {
		return "", errors.Wrapf(err, "failed to get commit pointed to by %s", upgradeRef)
	}

	if versionStreamRef == upgradeVersionSha {
		log.Logger().Infof(util.ColorInfo("No upgrade available"))
		return "", nil
	}
	log.Logger().Infof(util.ColorInfo("Upgrade available"))
	return upgradeVersionSha, nil
}

func (o *UpgradeBootOptions) checkoutNewBranch() (string, error) {
	localBranchUUID, err := uuid.NewV4()
	if err != nil {
		return "", errors.Wrapf(err, "creating UUID for local branch")
	}
	localBranch := localBranchUUID.String()
	err = o.Git().CreateBranch(o.Dir, localBranch)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create local branch %s", localBranch)
	}
	err = o.Git().Checkout(o.Dir, localBranch)
	if err != nil {
		return "", errors.Wrapf(err, "failed to checkout local branch %s", localBranch)
	}
	return localBranch, nil
}

func (o *UpgradeBootOptions) updateVersionStreamRef(upgradeRef string) error {
	requirements, requirementsFile, err := o.loadRequirementsConfig()
	if err != nil {
		return errors.Wrap(err, "failed to load requirements config")
	}

	if requirements.VersionStream.Ref != upgradeRef {
		log.Logger().Infof("Upgrading version stream ref to %s", upgradeRef)
		requirements.VersionStream.Ref = upgradeRef
		err = requirements.SaveConfig(requirementsFile)
		if err != nil {
			return errors.Wrapf(err, "failed to write version stream to %s", requirementsFile)
		}
		err = o.Git().AddCommitFiles(o.Dir, "feat: upgrade version stream", []string{requirementsFile})
		if err != nil {
			return errors.Wrapf(err, "failed to commit requirements file %s", requirementsFile)
		}
	}
	return nil
}

func (o *UpgradeBootOptions) updateBootConfig(versionStreamURL string, versionStreamRef string, bootConfigURL string, upgradeVersionSha string) error {
	configCloneDir, err := o.cloneBootConfig(bootConfigURL)
	if err != nil {
		return errors.Wrapf(err, "failed to clone boot config repo %s", bootConfigURL)
	}
	defer func() {
		err := os.RemoveAll(configCloneDir)
		if err != nil {
			log.Logger().Infof("Error removing tmpDir: %v", err)
		}
	}()

	currentSha, currentVersion, err := o.bootConfigRef(configCloneDir, versionStreamURL, versionStreamRef, bootConfigURL)
	if err != nil {
		return errors.Wrapf(err, "failed to get boot config ref for version stream: %s", versionStreamRef)
	}
	upgradeSha, upgradeVersion, err := o.bootConfigRef(configCloneDir, versionStreamURL, upgradeVersionSha, bootConfigURL)
	if err != nil {
		return errors.Wrapf(err, "failed to get boot config ref for version stream ref: %s", upgradeVersionSha)
	}

	// check if boot config upgrade available
	if upgradeSha == currentSha {
		log.Logger().Infof(util.ColorInfo("No boot config upgrade available"))
		return nil
	}
	log.Logger().Infof(util.ColorInfo("boot config upgrade available"))
	log.Logger().Infof("Upgrading from v%s to v%s", currentVersion, upgradeVersion)

	err = o.Git().FetchBranch(o.Dir, bootConfigURL, "master")
	if err != nil {
		return errors.Wrapf(err, "failed to fetch master of %s", bootConfigURL)
	}

	err = o.cherryPickCommits(configCloneDir, currentSha, upgradeSha)
	if err != nil {
		return errors.Wrap(err, "failed to cherry pick upgrade commits")
	}
	err = o.excludeFiles(currentSha)
	if err != nil {
		return errors.Wrap(err, "failed to exclude files from commit")
	}
	return nil
}

func (o *UpgradeBootOptions) bootConfigRef(dir string, versionStreamURL string, versionStreamRef string, configURL string) (string, string, error) {
	resolver, err := o.CreateVersionResolver(versionStreamURL, versionStreamRef)
	if err != nil {
		return "", "", errors.Wrapf(err, "failed to create version resolver %s", configURL)
	}
	configVersion, err := resolver.ResolveGitVersion(configURL)
	if err != nil {
		return "", "", errors.Wrapf(err, "failed to resolve config url %s", configURL)
	}
	cmtSha, err := o.Git().GetCommitPointedToByTag(dir, fmt.Sprintf("v%s", configVersion))
	if err != nil {
		return "", "", errors.Wrapf(err, "failed to get commit pointed to by %s", cmtSha)
	}
	return cmtSha, configVersion, nil
}

func (o *UpgradeBootOptions) cloneBootConfig(configURL string) (string, error) {
	cloneDir, err := ioutil.TempDir("", "")
	err = os.MkdirAll(cloneDir, util.DefaultWritePermissions)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create directory: %s", cloneDir)
	}

	err = o.Git().CloneBare(cloneDir, configURL)
	if err != nil {
		return "", errors.Wrapf(err, "failed to clone git URL %s to directory %s", configURL, cloneDir)
	}
	return cloneDir, nil
}

func (o *UpgradeBootOptions) cherryPickCommits(cloneDir, fromSha, toSha string) error {
	cmts := make([]gits.GitCommit, 0)
	cmts, err := o.Git().GetCommits(cloneDir, fromSha, toSha)
	if err != nil {
		return errors.Wrapf(err, "failed to get commits from %s", cloneDir)
	}

	log.Logger().Infof("cherry picking commits in the range %s..%s", fromSha, toSha)
	for i := len(cmts) - 1; i >= 0; i-- {
		commitSha := cmts[i].SHA
		commitMsg := cmts[i].Subject()

		err := o.Git().CherryPickTheirs(o.Dir, commitSha)
		if err != nil {
			msg := fmt.Sprintf("commit %s is a merge but no -m option was given.", commitSha)
			if !strings.Contains(err.Error(), msg) {
				return errors.Wrapf(err, "cherry-picking %s", commitSha)
			}
		} else {
			log.Logger().Infof("%s - %s", commitSha, commitMsg)
		}
	}
	return nil
}

func (o *UpgradeBootOptions) setupGitConfig(dir string) error {
	jxClient, devNs, err := o.JXClientAndDevNamespace()
	devEnv, err := kube.GetDevEnvironment(jxClient, devNs)
	if err != nil {
		return errors.Wrapf(err, "failed to get dev environment in namespace %s", devNs)
	}
	username := devEnv.Spec.TeamSettings.PipelineUsername
	email := devEnv.Spec.TeamSettings.PipelineUserEmail

	err = o.Git().SetUsername(dir, username)
	if err != nil {
		return errors.Wrapf(err, "failed to set username %s", username)
	}
	err = o.Git().SetEmail(dir, email)
	if err != nil {
		return errors.Wrapf(err, "failed to set email for %s", email)
	}
	return nil
}

func (o *UpgradeBootOptions) excludeFiles(commit string) error {
	excludedFiles := []string{"OWNERS"}
	err := o.Git().CheckoutCommitFiles(o.Dir, commit, excludedFiles)
	if err != nil {
		return errors.Wrap(err, "failed to checkout files")
	}
	err = o.Git().AddCommitFiles(o.Dir, "chore: exclude files from upgrade", excludedFiles)
	if err != nil && !strings.Contains(err.Error(), "nothing to commit") {
		return errors.Wrapf(err, "failed to commit excluded files %v", excludedFiles)
	}
	return nil
}

func (o *UpgradeBootOptions) raisePR() error {
	gitInfo, err := o.Git().Info(o.Dir)
	if err != nil {
		return errors.Wrap(err, "failed to get git info")
	}

	provider, err := o.gitProvider(gitInfo)
	if err != nil {
		return errors.Wrap(err, "failed to get git provider")
	}

	upstreamInfo, err := provider.GetRepository(gitInfo.Organisation, gitInfo.Name)
	if err != nil {
		return errors.Wrapf(err, "getting repository %s/%s", gitInfo.Organisation, gitInfo.Name)
	}

	details := gits.PullRequestDetails{
		BranchName: fmt.Sprintf("jx_boot_upgrade"),
		Title:      "feat(config): upgrade configuration",
		Message:    "Upgrade configuration",
	}

	filter := gits.PullRequestFilter{
		Labels: []string{
			boot.PullRequestLabel,
		},
	}
	_, err = gits.PushRepoAndCreatePullRequest(o.Dir, upstreamInfo, nil, "master", &details, &filter, false, details.Title, true, false, o.Git(), provider, []string{boot.PullRequestLabel})
	if err != nil {
		return errors.Wrapf(err, "failed to create PR for base %s and head branch %s", "master", details.BranchName)
	}
	return nil
}

func (o *UpgradeBootOptions) deleteLocalBranch(branch string) error {
	err := o.Git().Checkout(o.Dir, "master")
	if err != nil {
		return errors.Wrapf(err, "failed to checkout master branch")
	}
	err = o.Git().DeleteLocalBranch(o.Dir, branch)
	if err != nil {
		return errors.Wrapf(err, "failed to delete local branch %s", branch)
	}
	return nil
}

func (o *UpgradeBootOptions) cloneDevEnv() error {
	jxClient, devNs, err := o.JXClientAndDevNamespace()
	devEnv, err := kube.GetDevEnvironment(jxClient, devNs)
	if err != nil {
		return errors.Wrapf(err, "failed to get dev environment in namespace %s", devNs)
	}
	devEnvURL := devEnv.Spec.Source.URL

	cloneDir, err := ioutil.TempDir("", "")
	if err != nil {
		return errors.Wrapf(err, "failed to create tmp dir to clone dev env repo")
	}
	err = os.MkdirAll(cloneDir, util.DefaultWritePermissions)
	if err != nil {
		return errors.Wrapf(err, "failed to give write perms to tmp dir to clone dev env repo")
	}
	_, userAuth, err := o.pipelineUserAuth()
	if err != nil {
		return errors.Wrap(err, "failed to get pipeline user auth")
	}
	cloneURL, err := o.Git().CreateAuthenticatedURL(devEnvURL, userAuth)

	if err != nil {
		return errors.Wrapf(err, "failed to create directory for dev env clone: %s", cloneDir)
	}
	err = o.Git().Clone(cloneURL, cloneDir)
	if err != nil {
		return errors.Wrapf(err, "failed to clone git URL %s to directory %s", devEnvURL, cloneDir)
	}

	o.Dir = cloneDir
	return nil
}

func (o *UpgradeBootOptions) pipelineUserAuth() (*auth.AuthServer, *auth.UserAuth, error) {
	authConfigSvc, err := o.CreatePipelineUserGitAuthConfigService()
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create pipeline user git auth config service")
	}
	server, userAuth := authConfigSvc.Config().GetPipelineAuth()
	return server, userAuth, nil
}

func (o *UpgradeBootOptions) gitProvider(gitInfo *gits.GitRepository) (gits.GitProvider, error) {
	server, userAuth, err := o.pipelineUserAuth()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get pipeline user auth")
	}

	gitKind, err := o.GitServerKind(gitInfo)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get git kind")
	}

	provider, err := gitInfo.CreateProviderForUser(server, userAuth, gitKind, o.Git())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create provider for user")
	}
	return provider, nil
}
