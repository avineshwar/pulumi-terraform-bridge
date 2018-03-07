// Copyright 2016-2017, Pulumi Corporation.  All rights reserved.

package tfgen

import (
	"go/build"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml"
	"github.com/pkg/errors"
)

// GitInfo contains Git information about a provider.
type GitInfo struct {
	Repo   string // the Git repo for this provider.
	Tag    string // the Git tag info for this provider.
	Commit string // the Git commit info for this provider.
}

const (
	tfGitHub         = "github.com"
	tfProvidersOrg   = "terraform-providers"
	tfProviderPrefix = "terraform-provider"
)

// getGitInfo fetches the taggish and commitish info for a provider's repo.  It prefers to use a Gopkg.lock file, in
// case dep is being used to vendor, and falls back to looking at the raw Git repo using a standard GOPATH location
// otherwise.  If neither is found, an error is returned.
func getGitInfo(prov string) (*GitInfo, error) {
	if prov == "azure" {
		prov = "azurerm"
	}
	repo := tfGitHub + "/" + tfProvidersOrg + "/" + tfProviderPrefix + "-" + prov

	// First look for a Gopkg.lock file.
	pkglock, err := toml.LoadFile("Gopkg.lock")
	if err == nil {
		// If no error, attempt to use the file.  Otherwise, keep looking for a Git repo.
		if projs, isprojs := pkglock.Get("projects").([]*toml.Tree); isprojs {
			for _, proj := range projs {
				if name, isname := proj.Get("name").(string); isname && name == repo {
					var tag string
					if vers, isvers := proj.Get("version").(string); isvers {
						tag = vers
					}
					var commit string
					if revs, isrevs := proj.Get("revision").(string); isrevs {
						commit = revs
					}
					if tag != "" || commit != "" {
						return &GitInfo{
							Repo:   repo,
							Tag:    tag,
							Commit: commit,
						}, nil
					}
				}
			}
		}
	}

	// If that didn't work, try the GOPATH for a Git repo.
	repodir, err := getRepoDir(prov)
	if err != nil {
		return nil, err
	}

	// Make sure the target is actually a Git repository so we can fail with a pretty error if not.
	if _, staterr := os.Stat(filepath.Join(repodir, ".git")); staterr != nil {
		return nil, errors.Errorf("%v is not a Git repo, and no vendored copy was found", repodir)
	}

	// Now launch the Git commands.
	// nolint: gas, intentionally run `git` from the `$PATH`.
	descCmd := exec.Command("git", "describe", "--all", "--long")
	descCmd.Dir = repodir
	descOut, err := descCmd.Output()
	if err != nil {
		return nil, err
	} else if strings.HasSuffix(string(descOut), "\n") {
		descOut = descOut[:len(descOut)-1]
	}
	// nolint: gas, intentionally run `git` from the `$PATH`.
	showRefCmd := exec.Command("git", "show-ref", "HEAD")
	showRefCmd.Dir = repodir
	showRefOut, err := showRefCmd.Output()
	if err != nil {
		return nil, err
	} else if strings.HasSuffix(string(showRefOut), "\n") {
		showRefOut = showRefOut[:len(showRefOut)-1]
	}
	return &GitInfo{
		Repo:   repo,
		Tag:    string(descOut),
		Commit: string(showRefOut),
	}, nil
}

// getRepoDir gets the source repository for a given provider
func getRepoDir(prov string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if prov == "azure" {
		prov = "azurerm"
	}
	repo := path.Join(tfGitHub, tfProvidersOrg, tfProviderPrefix+"-"+prov)
	pkg, err := build.Import(repo, wd, build.FindOnly)
	if err != nil {
		return "", err
	}
	return pkg.Dir, nil
}
