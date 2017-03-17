/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mungers

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/oauth2"

	"k8s.io/test-infra/mungegithub/features"
	"k8s.io/test-infra/mungegithub/github"

	"encoding/base64"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/golang/glog"
	githubapi "github.com/google/go-github/github"
	"github.com/spf13/cobra"
)

var _ Munger = &BulkLGTM{}

const tokenName = "token"

func init() {
	RegisterMungerOrDie(&BulkLGTM{})
}

// BulkLGTM knows how to aggregate a large number of small PRs into a single page for
// easy bulk review.
type BulkLGTM struct {
	config          *github.Config
	lock            sync.Mutex
	currentPRList   map[int]*github.MungeObject
	maxDiff         int
	maxCommits      int
	maxChangedFiles int

	githubUser       string
	oauthStateString string
	// Set the token cookie to secure. This should only be true when testing.
	disableSecureCookie bool
	cookieDuration      time.Duration
}

// Munge implements the Munger interface
func (b *BulkLGTM) Munge(obj *github.MungeObject) {
	pr, isPr := obj.GetPR()
	if !isPr {
		return
	}
	glog.V(4).Infof("Found a PR: %#v", *pr)
	if pr.Mergeable == nil || !*pr.Mergeable {
		glog.V(4).Infof("PR is unknown, or not mergeable, skipping")
		return
	}
	if pr.Commits == nil || *pr.Commits > b.maxCommits {
		glog.V(4).Infof("PR has too many commits %d vs %d, skipping", *pr.Commits, b.maxCommits)
		return
	}
	if *pr.ChangedFiles > b.maxChangedFiles {
		glog.V(4).Infof("PR has too many changed files %d vs %d, skipping", *pr.ChangedFiles, b.maxChangedFiles)
		return
	}
	if *pr.Additions+*pr.Deletions > b.maxDiff {
		glog.V(4).Infof("PR has too many diffs %d vs %d, skipping", *pr.Additions+*pr.Deletions, b.maxDiff)
		return
	}
	if obj.HasLabel(lgtmLabel) {
		return
	}
	if !obj.HasLabel("cncf-cla: yes") {
		return
	}
	glog.V(4).Infof("Adding a PR: %d", *pr.Number)
	b.lock.Lock()
	defer b.lock.Unlock()
	if b.currentPRList == nil {
		b.currentPRList = map[int]*github.MungeObject{}
	}
	b.currentPRList[*pr.Number] = obj
}

// AddFlags implements the Munger interface
func (b *BulkLGTM) AddFlags(cmd *cobra.Command, config *github.Config) {
	cmd.Flags().IntVar(&b.maxDiff, "bulk-lgtm-max-diff", 10, "The maximum number of differences (additions + deletions) for PRs to include in the bulk LGTM list")
	cmd.Flags().IntVar(&b.maxChangedFiles, "bulk-lgtm-changed-files", 1, "The maximum number of changed files for PRs to include in the bulk LGTM list")
	cmd.Flags().IntVar(&b.maxCommits, "bulk-lgtm-max-commits", 1, "The maximum number of commits for PRs to include in the bulk LGTM list")
	cmd.Flags().StringVar(&b.githubUser, "bulk-lgtm-github-user", "", "Username on github to use for bulk-lgtm")
	cmd.Flags().DurationVar(&b.cookieDuration, "bulk-lgtm-cookie-duration", 24*time.Hour, "The duration for the cookie used to store github credentials.")
	cmd.Flags().BoolVar(&b.disableSecureCookie, "bulk-lgtm-insecure-disable-secure-cookie", false, "If true, the cookie storing github credentials will be allowed on http")
}

// Name implements the Munger interface
func (b *BulkLGTM) Name() string {
	return "bulk-lgtm"
}

// RequiredFeatures implements the Munger interface
func (b *BulkLGTM) RequiredFeatures() []string {
	return nil
}

// Initialize implements the Munger interface
func (b *BulkLGTM) Initialize(config *github.Config, features *features.Features) error {
	b.config = config

	if len(config.Address) > 0 {
		http.HandleFunc("/bulkprs/prs", b.ServePRs)
		http.HandleFunc("/bulkprs/prdiff", b.ServePRDiff)
		http.HandleFunc("/bulkprs/lgtm", b.ServeLGTM)
		http.HandleFunc("/bulkprs/auth", b.ServeLogin)
		http.HandleFunc("/bulkprs/callback", b.ServeCallback)
		http.HandleFunc("/bulkprs/user", b.ServeUser)
		if len(config.WWWRoot) > 0 {
			http.Handle("/", gziphandler.GzipHandler(http.FileServer(http.Dir(config.WWWRoot))))
		}

		go http.ListenAndServe(config.Address, nil)
	}

	return nil
}

// EachLoop implements the Munger interface
func (b *BulkLGTM) EachLoop() error {
	return nil
}

// FindPR finds a PR in the list given its number
func (b *BulkLGTM) FindPR(number int) *github.MungeObject {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.currentPRList[number]
}

// ServeLGTM serves the LGTM page over HTTP
func (b *BulkLGTM) ServeLGTM(res http.ResponseWriter, req *http.Request) {
	githubClient, err := makeGithubClient(req)
	if err != nil {
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(err.Error()))
		return
	}
	if githubClient == nil {
		res.WriteHeader(http.StatusBadRequest)
		res.Write([]byte("Not logged in."))
		return
	}
	prNumber, err := strconv.Atoi(req.URL.Query().Get("number"))
	if err != nil {
		res.Header().Set("Content-type", "text/plain")
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(err.Error()))
		return
	}
	obj := b.FindPR(prNumber)
	if obj == nil {
		res.Header().Set("Content-type", "text/plain")
		res.WriteHeader(http.StatusNotFound)
		return
	}
	user, _, err := githubClient.Users.Get("")
	if err != nil {
		res.Header().Set("Content-type", "text/plain")
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(err.Error()))
		return
	}
	if _, _, err := githubClient.Issues.AddAssignees(b.config.Org, b.config.Project, prNumber, []string{*user.Login}); err != nil {
		res.Header().Set("Content-type", "text/plain")
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(err.Error()))
		return
	}
	msg := "/lgtm\n\n/release-note-none\n\nLGTM + release-note-none from the bulk LGTM tool"
	if _, _, err := githubClient.Issues.CreateComment(b.config.Org, b.config.Project, prNumber, &githubapi.IssueComment{Body: &msg}); err != nil {
		res.Header().Set("Content-type", "text/plain")
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(err.Error()))
		return
	}
	res.Header().Set("Content-type", "text/plain")
	res.WriteHeader(http.StatusOK)
}

// ServePRDiff serves the difs in the PR over HTTP
func (b *BulkLGTM) ServePRDiff(res http.ResponseWriter, req *http.Request) {
	prNumber, err := strconv.Atoi(req.URL.Query().Get("number"))
	if err != nil {
		res.Header().Set("Content-type", "text/plain")
		res.WriteHeader(http.StatusInternalServerError)
		return
	}
	obj := b.FindPR(prNumber)
	if obj == nil {
		res.Header().Set("Content-type", "text/plain")
		res.WriteHeader(http.StatusNotFound)
		return
	}
	pr, _ := obj.GetPR()
	resp, err := http.Get(*pr.DiffURL)
	if err != nil {
		res.Header().Set("Content-type", "text/plain")
		res.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		res.Header().Set("Content-type", "text/plain")
		res.WriteHeader(http.StatusInternalServerError)
		return
	}
	res.Header().Set("Content-Type", "text/plain")
	res.WriteHeader(http.StatusOK)
	res.Write(data)
}

// ServePRs serves the current PR list over HTTP
func (b *BulkLGTM) ServePRs(res http.ResponseWriter, req *http.Request) {
	b.lock.Lock()
	defer b.lock.Unlock()
	var data []byte
	var err error
	if b.currentPRList == nil {
		data = []byte("[]")
	} else {
		arr := make([]*githubapi.PullRequest, len(b.currentPRList))
		ix := 0
		for key := range b.currentPRList {
			arr[ix], _ = b.currentPRList[key].GetPR()
			ix = ix + 1
		}
		data, err = json.Marshal(arr)
		if err != nil {
			res.Header().Set("Content-type", "text/plain")
			res.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	res.WriteHeader(http.StatusOK)
	res.Write(data)
}

var (
	githubOauthConfig = &oauth2.Config{
		RedirectURL:  "http://localhost:8080/bulkprs/callback",
		ClientID:     "8fcec56965d35fe888cd",
		ClientSecret: "e203a919d839b212064165855e67a80e86d35470",
		Scopes:       []string{"user", "public_repo"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://github.com/login/oauth/authorize",
			TokenURL: "https://github.com/login/oauth/access_token",
		},
	}
)

func randomString() (string, error) {
	c := 8
	b := make([]byte, c)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var num int64
	if err := binary.Read(bytes.NewReader(b), binary.LittleEndian, &num); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", num), nil
}

func (b *BulkLGTM) ServeLogin(res http.ResponseWriter, req *http.Request) {
	logout := req.FormValue("logout")
	if "true" == logout {
		http.SetCookie(res, &http.Cookie{
			Name:   tokenName,
			Value:  "",
			MaxAge: -1,
		})

		res.WriteHeader(http.StatusOK)
		res.Write([]byte("OK"))
		return
	}
	redirectUri := req.FormValue("redirect")
	salt, err := randomString()
	if err != nil {
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(err.Error()))
		return
	}
	b.oauthStateString = fmt.Sprintf("%s/%s", salt, redirectUri) // should be crypto random
	url := githubOauthConfig.AuthCodeURL(b.oauthStateString)
	http.Redirect(res, req, url, http.StatusTemporaryRedirect)
}

func (b *BulkLGTM) ServeCallback(res http.ResponseWriter, req *http.Request) {
	state := req.FormValue("state")
	if state != b.oauthStateString {
		glog.Errorf("invalid oauth state, expected '%s', got '%s'\n", b.oauthStateString, state)
		http.Redirect(res, req, "/bulkprs/auth", http.StatusTemporaryRedirect)
		return
	}

	code := req.FormValue("code")
	token, err := githubOauthConfig.Exchange(oauth2.NoContext, code)
	if err != nil {
		glog.Errorf("Code exchange failed with '%s'\n", err)
		http.Redirect(res, req, "/bulkprs/auth", http.StatusTemporaryRedirect)
		return
	}

	data, err := json.Marshal(token)
	if err != nil {
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(err.Error()))
		return
	}
	encoded := base64.URLEncoding.EncodeToString(data)
	cookie := &http.Cookie{
		Name:   tokenName,
		Value:  encoded,
		Secure: !b.disableSecureCookie,
	}
	if !b.disableSecureCookie {
		cookie.Expires = time.Now().Add(b.cookieDuration)
	}
	http.SetCookie(res, cookie)

	ix := strings.Index(state, "/")
	if ix == -1 {
		res.WriteHeader(http.StatusOK)
		fmt.Fprintf(res, "OK\n")
		return
	}
	url := state[ix+1:]
	http.Redirect(res, req, url, http.StatusTemporaryRedirect)
}

func makeGithubClient(req *http.Request) (*githubapi.Client, error) {
	c, err := req.Cookie(tokenName)
	if err != nil {
		if err == http.ErrNoCookie {
			return nil, nil
		}
		return nil, err
	}
	if c == nil || len(c.Value) == 0 {
		return nil, nil
	}

	data, err := base64.URLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil, err
	}
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	ts := oauth2.StaticTokenSource(&token)
	tc := oauth2.NewClient(oauth2.NoContext, ts)
	return githubapi.NewClient(tc), nil
}

func (b *BulkLGTM) ServeUser(res http.ResponseWriter, req *http.Request) {
	githubClient, err := makeGithubClient(req)
	if err != nil {
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(err.Error()))
		return
	}
	if githubClient == nil {
		res.WriteHeader(http.StatusNotFound)
		res.Write([]byte("{ \"login\": \"unknown\"}"))
		return
	}
	user, _, err := githubClient.Users.Get("")
	if err != nil {
		res.WriteHeader(http.StatusInternalServerError)
		res.Write([]byte(err.Error()))
		return
	}
	res.WriteHeader(http.StatusOK)
	fmt.Fprintf(res, "{ \"login\": \"%s\" }", *user.Login)
}
