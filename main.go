package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"

	"github.com/AlecAivazis/survey/v2"
	markdown "github.com/MichaelMure/go-term-markdown"
	"github.com/clok/kemba"
	semv "github.com/linyows/git-semv"
	"github.com/tsuyoshiwada/go-gitlog"
	cli "github.com/urfave/cli/v2"
)

func RepoIsClean() (bool, error) {
	res, err := exec.Command("git", "status", "-s").Output()
	if err != nil {
		return false, err
	}
	return len(res) == 0, nil
}

func RepoIsMasterOrMain() (bool, error) {
	res, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return false, err
	}
	branchName := strings.Trim(string(res), "\n")
	if branchName == "master" || branchName == "main" {
		return true, nil
	}
	return false, nil
}

func GetLastCommit() (string, error) {
	res, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	commitName := strings.Trim(string(res), "\n")
	return commitName, nil
}

func RepoFetchTags() error {
	_, err := exec.Command("git", "fetch", "--tags", "--force").Output()
	return err
}

func CheckIfTagExists(tagName string) error {
	_, err := exec.Command("git", "rev-parse", tagName).Output()
	return err
}

func RepoCreateTag(tagName string) error {
	_, err := exec.Command("git", "tag", tagName).Output()
	if err != nil {
		return err
	}
	_, err = exec.Command("git", "push", "origin", tagName).Output()
	return err
}

func GenerateGithubRelease(releaseTag string, changeLog string) error {
	_, err := exec.Command("gh", "release", "create", releaseTag, "--notes", changeLog, "-t", releaseTag).Output()
	return err
}

func generateMarkdownChangelog(fromTag string, untilTag string) (string, error) {
	git := gitlog.New(&gitlog.Config{})
	var commits []*gitlog.Commit
	var err error
	if fromTag == "" {
		commits, err = git.Log(nil, nil)
	} else {
		lastCommit, err := GetLastCommit()
		if err != nil {
			return "", err
		}
		commits, err = git.Log(&gitlog.RevRange{
			Old: fromTag,
			New: lastCommit,
		}, nil)
		if err != nil {
			return "", err
		}
	}

	if err != nil {
		return "", err
	}
	tmplData := map[string]interface{}{
		"ReleaseTag": untilTag,
		"CreatedAt":  time.Now(),
		"Commits":    commits,
	}

	var b bytes.Buffer
	err = mdTmpl.Execute(&b, tmplData)
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

func validateVersion(version string) error {
	valid := []string{"patch", "minor", "major"}
	err := errors.New("Version has to be oneOf: patch, minor or major")
	if version == "" {
		return err
	}
	for _, v := range valid {
		if v == version {
			return nil
		}
	}
	return err
}

func deployNewVersion(nextVersion string, buildName string) error {
	l := kemba.New("deloy")

	l.Printf("Starting deployment %s for %s", nextVersion, buildName)

	// Check if repo is clean
	l.Println("Checking if repo is clean")
	isClean, err := RepoIsClean()
	if err != nil {
		return err
	}
	if !isClean {
		return errors.New("Please make sure there are no changes")
	}
	l.Println("Repo is clean")

	// Check if we are on master
	l.Println("Check if repo is master or main")
	isMasterOrMain, err := RepoIsMasterOrMain()
	if err != nil {
		return err
	}
	if !isMasterOrMain {
		return errors.New("Releases are allowed to tag from master/main branch")
	}

	// Fetch latest remote tags
	l.Println("Fetching Tags")
	err = RepoFetchTags()
	if err != nil {
		return err
	}

	// Get the latest Tag
	l.Println("Getting Latest Tag")
	latest, err := semv.Latest()
	if err != nil {
		return err
	}
	// add the build name to the Tag
	if buildName != "" {
		_, _ = latest.Build(buildName)
		// we need to check if the latest with build tag exists
		// if not fall back to the latest that does
		l.Println("Checking of last tag exists")
		err := CheckIfTagExists(latest.String())
		if err != nil {
			latest, err = semv.Latest()
			if err != nil {
				return err
			}
		}
	}

	// Get the next Tag
	next := latest.Next(nextVersion)
	if buildName != "" {
		_, _ = next.Build(buildName)
	}

	nextReleaseTag := next.String()

	// generate changelog
	l.Printf("Generating markdown - fromTag: %s untilTag: %s", latest.String(), nextReleaseTag)
	chglog, err := generateMarkdownChangelog(latest.String(), nextReleaseTag)
	if err != nil {
		return err
	}
	fmt.Println(string("\nCHANGELOG:\n"))
	result := markdown.Render(string(chglog), 80, 6)
	fmt.Println(string(result))
	deploy := false
	prompt := &survey.Confirm{
		Message: "Do you want to deploy: " + nextReleaseTag + " ?",
		Default: true,
	}

	err = survey.AskOne(prompt, &deploy)
	if err != nil {
		return err
	}

	if !deploy {
		return nil
	}
	l.Println("Generating and pushing tag")
	err = RepoCreateTag(nextReleaseTag)
	if err != nil {
		return err
	}
	l.Println("Generating github release")
	err = GenerateGithubRelease(nextReleaseTag, chglog)
	if err != nil {
		return err
	}

	l.Println("done deploying")

	return nil
}

var (
	mdTmpl  *template.Template
	tmplStr = `## {{ .ReleaseTag }} {{.CreatedAt.Format "02.01.2006"}}
{{ range .Commits -}}
- [{{.Hash.Short}}](../../commit/{{.Hash.Long}}) {{ .Subject }} ({{ .Author.Name}}, {{.Author.Date.Format "02.01.2006"}})
{{ end }}
`
)

func init() {
	var err error
	mdTmpl, err = template.New("md-changelog").Parse(tmplStr)
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	var buildName string
	var version string
	app := &cli.App{
		Usage:     "a monorepo deploy helper",
		UsageText: "deploy --version minor --name myservice",
		Name:      "deploy",
		Description: `Generates a new release by:
- Creating and pushing a semver git tag (optional with a name)
- Generating a changelog
- Generates a new github release
`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "version",
				Aliases:     []string{"v"},
				Value:       "patch",
				Usage:       "Version you want to deploy, can be: patch, minor, major",
				Destination: &version,
			},
			&cli.StringFlag{
				Name:        "name",
				Aliases:     []string{"n"},
				Usage:       "Optional: Service prefix for the tag",
				Destination: &buildName,
			},
		},
		Action: func(c *cli.Context) error {
			err := validateVersion(version)
			if err != nil {
				return err
			}
			return deployNewVersion(version, buildName)
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}
