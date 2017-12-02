package lib

import (
	"strings"
	"time"

	"github.com/andygrunwald/go-jira"
	"github.com/coreos/issue-sync/cfg"
	"github.com/coreos/issue-sync/lib/clients"
	"github.com/google/go-github/github"
	"regexp"
)

// dateFormat is the format used for the Last IS Update field
const dateFormat = "2006-01-02T15:04:05.0-0700"

// CompareIssues gets the list of GitHub issues updated since the `since` date,
// gets the list of JIRA issues which have GitHub ID custom fields in that list,
// then matches each one. If a JIRA issue already exists for a given GitHub issue,
// it calls UpdateIssue; if no JIRA issue already exists, it calls CreateIssue.
func CompareIssues(config cfg.Config, ghClient clients.GitHubClient, jiraClient clients.JIRAClient) error {
	log := config.GetLogger()

	log.Debug("Collecting issues")

	ghIssues, err := ghClient.ListIssues()
	if err != nil {
		return err
	}

	if len(ghIssues) == 0 {
		log.Info("There are no GitHub issues; exiting")
		return nil
	}

	ids := make([]int, len(ghIssues))
	for i, v := range ghIssues {
		ids[i] = v.GetID()
	}

	jiraIssues, err := jiraClient.ListIssues(ids)
	if err != nil {
		return err
	}

	log.Debug("Collected all JIRA issues")

	for _, ghIssue := range ghIssues {
		found := false
		ghTranslatedIssue := NewTranslatedIssue(ghIssue)
		for _, jIssue := range jiraIssues {
			id, _ := jIssue.Fields.Unknowns.Int(config.GetFieldKey(cfg.GitHubID))
			if int64(*ghIssue.ID) == id {
				found = true
				if err := UpdateIssue(config, ghTranslatedIssue, jIssue, ghClient, jiraClient); err != nil {
					log.Errorf("Error updating issue %s. Error: %v", jIssue.Key, err)
				}
				break
			}
		}
		if !found {
			if err := CreateIssue(config, ghTranslatedIssue, ghClient, jiraClient); err != nil {
				log.Errorf("Error creating issue for #%d. Error: %v", *ghIssue.Number, err)
			}
		}
	}

	return nil
}

// DidIssueChange tests each of the relevant fields on the provided JIRA and GitHub issue
// and returns whether or not they differ.
func DidIssueChange(config cfg.Config, ghIssue TranslatedIssue, jIssue jira.Issue) bool {
	log := config.GetLogger()

	log.Debugf("Comparing GitHub issue #%d and JIRA issue %s", ghIssue.GetNumber(), jIssue.Key)

	anyDifferent := false

	anyDifferent = anyDifferent || (ghIssue.GetTitle() != jIssue.Fields.Summary)
	anyDifferent = anyDifferent || (ghIssue.GetTranslatedBody() != jIssue.Fields.Description)

	key := config.GetFieldKey(cfg.GitHubStatus)
	field, err := jIssue.Fields.Unknowns.String(key)
	if err != nil || *ghIssue.State != field {
		anyDifferent = true
	}

	key = config.GetFieldKey(cfg.GitHubReporter)
	field, err = jIssue.Fields.Unknowns.String(key)
	if err != nil || *ghIssue.User.Login != field {
		anyDifferent = true
	}

	labels := make([]string, len(ghIssue.Labels))
	for i, l := range ghIssue.Labels {
		labels[i] = *l.Name
	}

	key = config.GetFieldKey(cfg.GitHubLabels)
	field, err = jIssue.Fields.Unknowns.String(key)
	if err != nil && strings.Join(labels, ",") != field {
		anyDifferent = true
	}

	log.Debugf("Issues have any differences: %b", anyDifferent)

	return anyDifferent
}

// UpdateIssue compares each field of a GitHub issue to a JIRA issue; if any of them
// differ, the differing fields of the JIRA issue are updated to match the GitHub
// issue.
func UpdateIssue(config cfg.Config, ghIssue TranslatedIssue, jIssue jira.Issue, ghClient clients.GitHubClient, jClient clients.JIRAClient) error {
	log := config.GetLogger()

	log.Debugf("Updating JIRA %s with GitHub #%d", jIssue.Key, *ghIssue.Number)

	var issue jira.Issue

	if DidIssueChange(config, ghIssue, jIssue) {
		fields := jira.IssueFields{}
		fields.Unknowns = map[string]interface{}{}

		fields.Summary = ghIssue.GetTitle()
		fields.Description = ghIssue.GetTranslatedBody()
		fields.Unknowns[config.GetFieldKey(cfg.GitHubStatus)] = ghIssue.GetState()
		fields.Unknowns[config.GetFieldKey(cfg.GitHubReporter)] = ghIssue.User.GetLogin()

		labels := make([]string, len(ghIssue.Labels))
		for i, l := range ghIssue.Labels {
			labels[i] = l.GetName()
		}
		fields.Unknowns[config.GetFieldKey(cfg.GitHubLabels)] = strings.Join(labels, ",")

		// https://developer.atlassian.com/jiradev/jira-apis/about-the-jira-rest-apis/jira-rest-api-tutorials/jira-rest-api-example-create-issue
		// DateTime has the format 2011-10-19T10:29:29.908+1100
		fields.Unknowns[config.GetFieldKey(cfg.LastISUpdate)] = time.Now().UTC().Format(dateFormat)

		fields.Type = jIssue.Fields.Type

		issue = jira.Issue{
			Fields: &fields,
			Key:    jIssue.Key,
			ID:     jIssue.ID,
		}

		var err error
		issue, err = jClient.UpdateIssue(issue)
		if err != nil {
			return err
		}

		log.Debugf("Successfully updated JIRA issue %s!", jIssue.Key)
	} else {
		log.Debugf("JIRA issue %s is already up to date!", jIssue.Key)
	}

	issue, err := jClient.GetIssue(jIssue.Key)
	if err != nil {
		log.Debugf("Failed to retrieve JIRA issue %s!", jIssue.Key)
		return err
	}

	if err := CompareComments(config, ghIssue.Issue, issue, ghClient, jClient); err != nil {
		return err
	}

	return nil
}

// CreateIssue generates a JIRA issue from the various fields on the given GitHub issue, then
// sends it to the JIRA API.
func CreateIssue(config cfg.Config, issue TranslatedIssue, ghClient clients.GitHubClient, jClient clients.JIRAClient) error {
	log := config.GetLogger()

	log.Debugf("Creating JIRA issue based on GitHub issue #%d", *issue.Number)

	fields := jira.IssueFields{
		Type: jira.IssueType{
			Name: "Task", // TODO: Determine issue type
		},
		Project:     config.GetProject(ghClient.GetRepo()),
		Summary:     issue.GetTitle(),
		Description: issue.GetTranslatedBody(),
		Unknowns:    map[string]interface{}{},
	}

	fields.Unknowns[config.GetFieldKey(cfg.GitHubID)] = issue.GetID()
	fields.Unknowns[config.GetFieldKey(cfg.GitHubNumber)] = issue.GetNumber()
	fields.Unknowns[config.GetFieldKey(cfg.GitHubStatus)] = issue.GetState()
	fields.Unknowns[config.GetFieldKey(cfg.GitHubReporter)] = issue.User.GetLogin()

	strs := make([]string, len(issue.Labels))
	for i, v := range issue.Labels {
		strs[i] = *v.Name
	}
	fields.Unknowns[config.GetFieldKey(cfg.GitHubLabels)] = strings.Join(strs, ",")

	// https://developer.atlassian.com/jiradev/jira-apis/about-the-jira-rest-apis/jira-rest-api-tutorials/jira-rest-api-example-create-issue
	// DateTime has the format 2011-10-19T10:29:29.908+1100
	fields.Unknowns[config.GetFieldKey(cfg.LastISUpdate)] = time.Now().UTC().Format(dateFormat)

	jIssue := jira.Issue{
		Fields: &fields,
	}

	jIssue, err := jClient.CreateIssue(jIssue)
	if err != nil {
		return err
	}

	jIssue, err = jClient.GetIssue(jIssue.Key)
	if err != nil {
		return err
	}

	log.Debugf("Created JIRA issue %s!", jIssue.Key)

	if err := CompareComments(config, issue.Issue, jIssue, ghClient, jClient); err != nil {
		return err
	}

	return nil
}

type TranslatedIssue struct {

	github.Issue
	TranslatedBody *string
}

func NewTranslatedIssue(issue github.Issue) TranslatedIssue {
	body := GitHubToJiraBody(*(issue.Body))
	return TranslatedIssue{issue, &body}
}

func (i *TranslatedIssue) GetTranslatedBody() string {
	if i == nil || i.TranslatedBody == nil {
		return ""
	}
	return *i.TranslatedBody
}

// Headings
var regexH6 = regexp.MustCompile(`(?m)^###### (.*)$`)
var regexH5 = regexp.MustCompile(`(?m)^##### (.*)$`)
var regexH4 = regexp.MustCompile(`(?m)^#### (.*)$`)
var regexH3 = regexp.MustCompile(`(?m)^### (.*)$`)
var regexH2 = regexp.MustCompile(`(?m)^## (.*)$`)
var regexH1 = regexp.MustCompile(`(?m)^# (.*)$`)

// Text Effects
var regexStrong1 = regexp.MustCompile(`(?U)\*\*([\s\S]*)\*\*`)			// **strong**
var regexStrong2 = regexp.MustCompile(`(?U)__([\s\S]*)__`)				// __strong__
var regexEmphasis1 = regexp.MustCompile(`(?U)\*([\s\S]*)\*`)			// *emphasis*
var regexEmphasis2 = regexp.MustCompile(`(?U)_([\s\S]*)_`)				// _emphasis_
var regexCitation = regexp.MustCompile(`(?U)<cite>([\s\S]*)<cite>`)		// <cite>citation<cite>
var regexDeleted = regexp.MustCompile(`(?U)~~([\s\S])~~`)				// ~~deleted~~
var regexInserted = regexp.MustCompile(`(?U)<ins>([\s\S]*)<ins>`)		// <ins>insertion<ins>
var regexSuperscript = regexp.MustCompile(`(?U)<sup>([\s\S]*)<sup>`)	// <sup>superscript<sup>
var regexSubscript = regexp.MustCompile(`(?U)<sub>([\s\S]*)<sub>`)		// <sub>subscript<sub>
var regexMonospaced = regexp.MustCompile("(?U)`([\\s\\S]*)`")			// `monospaced`
var regexQuote = regexp.MustCompile(`(?m)^>\s+(.*)$`)					// > quote

// Links
var regexImage = regexp.MustCompile(`(?U)!\[(.*)\]\((.*)\)`)			// ![alt](url)
var regexURL = regexp.MustCompile(`(?U)<(.*)>`)							// <url>
var regexAltURL = regexp.MustCompile(`(?U)\[(.*)\]\((.*)\)`)			// [alt](url)

// Advanced Formatting
var regexCode = regexp.MustCompile("(?mU)^\\`\\`\\`(\\w+)$\\n([\\s\\S]*)\\n^\\`\\`\\`")
var regexNoFormat = regexp.MustCompile("(?mU)^\\`\\`\\`$\\n([\\s\\S]*)\\n^\\`\\`\\`")

// TODO: Tables

// JIRA and GitHub (Markdown) have different markups. Translate from GitHub (Markdown) to JIRA.
// See https://jira.atlassian.com/secure/WikiRendererHelpAction.jspa?section=all
func GitHubToJiraBody(body string) string {

	// Headings
	body = regexH6.ReplaceAllString(body, "h6. $1")
	body = regexH5.ReplaceAllString(body, "h5. $1")
	body = regexH4.ReplaceAllString(body, "h3. $1")
	body = regexH3.ReplaceAllString(body, "h3. $1")
	body = regexH2.ReplaceAllString(body, "h2. $1")
	body = regexH1.ReplaceAllString(body, "h1. $1")

	// Text Effects
	body = regexStrong1.ReplaceAllString(body, "*$1*")
	body = regexStrong2.ReplaceAllString(body, "*$1*")
	body = regexEmphasis1.ReplaceAllString(body, "_$1_")
	body = regexEmphasis2.ReplaceAllString(body, "_$1_")
	body = regexCitation.ReplaceAllString(body, "??$1??")
	body = regexDeleted.ReplaceAllString(body, "-$1-")
	body = regexInserted.ReplaceAllString(body, "+$1+")
	body = regexSuperscript.ReplaceAllString(body, "^$1^")
	body = regexSubscript.ReplaceAllString(body, "~$1~")
	body = regexMonospaced.ReplaceAllString(body, "{{$1}}")
	body = regexQuote.ReplaceAllString(body, "bq. $1")

	// Links
	body = regexImage.ReplaceAllString(body, "!$2|width=600!")
	body = regexURL.ReplaceAllString(body, "[$1]")
	body = regexAltURL.ReplaceAllString(body, "[$1|$2]")

	// Advanced Formatting
	body = regexCode.ReplaceAllString(body, `{code:$1}\n$2\n{code}`)
	body = regexNoFormat.ReplaceAllString(body, `{noformat}\n$2\n{noformat}`)

	return body
}

