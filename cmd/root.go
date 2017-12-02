package cmd

import (
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/andygrunwald/go-jira"
	"github.com/coreos/issue-sync/cfg"
	"github.com/coreos/issue-sync/lib"
	"github.com/coreos/issue-sync/lib/clients"
	"github.com/spf13/cobra"
)

// Execute provides a single function to run the root command and handle errors.
func Execute() {
	// Create a temporary logger that we can use if an error occurs before the real one is instantiated.
	log := logrus.New()
	if err := RootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

// RootCmd represents the command itself and configures it.
var RootCmd = &cobra.Command{
	Use:   "issue-sync [options]",
	Short: "A tool to synchronize GitHub and JIRA issues",
	Long:  "Full docs coming later; see https://github.com/coreos/issue-sync",
	RunE: func(cmd *cobra.Command, args []string) error {
		config, err := cfg.NewConfig(cmd)
		if err != nil {
			return err
		}

		log := config.GetLogger()

		// Create a temporary JIRA client which we can use to populate the
		// configuration object with all of the JIRA settings (projects,
		// field IDs, etc.)
		rootJCli, err := clients.NewJIRAClient(config, jira.Project{})
		if err != nil {
			return err
		}
		config.LoadJIRAConfig(rootJCli.GetClient())

		for {
			for _, repo := range config.GetRepoList() {
				ghClient, err := clients.NewGitHubClient(config, repo)
				if err != nil {
					return err
				}
				jiraClient, err := clients.NewJIRAClient(config, config.GetProject(repo))
				if err != nil {
					return err
				}

				if err := lib.CompareIssues(config, ghClient, jiraClient); err != nil {
					return err
				}
			}
			if !config.IsDryRun() {
				if err := config.SaveConfig(); err != nil {
					log.Error(err)
				}
			}
			if !config.IsDaemon() {
				return nil
			}
			<-time.After(config.GetDaemonPeriod())
		}
	},
}

func init() {
	RootCmd.PersistentFlags().String("log-level", logrus.InfoLevel.String(), "Set the global log level")
	RootCmd.PersistentFlags().String("config", "", "Config file (default is $HOME/.issue-sync.json)")
	RootCmd.PersistentFlags().StringP("github-token", "t", "", "Set the API Token used to access the GitHub repo")
	RootCmd.PersistentFlags().StringP("jira-user", "u", "", "Set the JIRA username to authenticate with")
	RootCmd.PersistentFlags().StringP("jira-pass", "p", "", "Set the JIRA password to authenticate with")
	RootCmd.PersistentFlags().StringP("repo-name", "r", "", "Set the repository path (should be form owner/repo)")
	RootCmd.PersistentFlags().StringP("jira-uri", "U", "", "Set the base uri of the JIRA instance")
	RootCmd.PersistentFlags().StringP("jira-project", "P", "", "Set the key of the JIRA project")
	RootCmd.PersistentFlags().StringP("since", "s", "1970-01-01T00:00:00+0000", "Set the day that the update should run forward from")
	RootCmd.PersistentFlags().BoolP("dry-run", "d", false, "Print out actions to be taken, but do not execute them")
	RootCmd.PersistentFlags().DurationP("timeout", "T", time.Minute, "Set the maximum timeout on all API calls")
	RootCmd.PersistentFlags().Duration("period", 0, "How often to synchronize; set to 0 for one-shot mode")
}
