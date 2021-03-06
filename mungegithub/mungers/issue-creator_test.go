/*
Copyright 2017 The Kubernetes Authors.

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
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	githubapi "github.com/google/go-github/github"
	"k8s.io/test-infra/mungegithub/github"
	"k8s.io/test-infra/mungegithub/mungers/testowner"
)

// fakeClient implements the RepoClient interface in order to be substituted for a
// github.Config github client when creating an IssueCreator.
type fakeClient struct {
	userName   string
	repoLabels []string
	issues     []*githubapi.Issue
	dryrun     bool
	org        string
	project    string
	t          *testing.T
}

func (c *fakeClient) GetUser(login string) (*githubapi.User, error) {
	if login == "" {
		return &githubapi.User{Login: &c.userName}, nil
	}
	return nil, fmt.Errorf("Fake Client is only able to retrieve the current authenticated user in its current state.")
}

func (c *fakeClient) GetLabels() ([]*githubapi.Label, error) {
	return makeLabelSlice(c.repoLabels), nil
}

func (c *fakeClient) ListAllIssues(options *githubapi.IssueListByRepoOptions) ([]*githubapi.Issue, error) {
	return c.issues, nil
}

func (c *fakeClient) NewIssue(title, body string, labels, owners []string) (*github.MungeObject, error) {
	// Check if labels are valid.
	for _, label := range labels {
		found := false
		for _, validLabel := range c.repoLabels {
			if validLabel == label {
				found = true
				break
			}
		}
		if !found {
			c.t.Errorf("%s is not a valid label!\n", label)
		}
	}

	issue := makeTestIssue(title, body, "open", labels, owners, len(c.issues))

	c.issues = append(c.issues, issue)
	obj := &github.MungeObject{Issue: issue}
	return obj, nil
}

func (c *fakeClient) isDryRun() bool {
	return c.dryrun
}

func (c *fakeClient) getOrg() string {
	return c.org
}

func (c *fakeClient) getProject() string {
	return c.project
}

func (c *fakeClient) RealConfig() *github.Config {
	return nil
}

// Verify checks that exactly 1 issue in c.issues matches the parameters and that no
// issues in c.issues have an empty body string (since that means they shouldn't have been created).
func (c *fakeClient) Verify(title, body string, owners, labels []string) bool {
	matchCount := 0
	for _, issue := range c.issues {
		if *issue.Title != title || *issue.Body != body {
			continue
		}
		// Verify that owners matches Assignees.
		assignees := make([]string, len(issue.Assignees))
		for i := 0; i < len(issue.Assignees); i++ {
			assignees[i] = *issue.Assignees[i].Login
		}
		if !stringSlicesEqual(assignees, owners) {
			continue
		}
		// Verify that labels matches issue.Labels.
		issueLabels := make([]string, len(issue.Labels))
		for i := 0; i < len(issue.Labels); i++ {
			issueLabels[i] = *issue.Labels[i].Name
		}
		if !stringSlicesEqual(issueLabels, labels) {
			continue
		}
		matchCount++
	}
	return matchCount == 1
}

type fakeIssue struct {
	title, body, id string
	labels, owners  []string
	priority        string // A value of "" indicates no priority is set.
}

func (i *fakeIssue) Title() string {
	return i.title
}

func (i *fakeIssue) Body(closed []*githubapi.Issue) string {
	// the functionality to check that there are no recently closed issues on github for a cluster is
	// part of the TriageFiler code and is tested in triage-filer_test.go
	// we ignore the param here
	return i.body
}

func (i *fakeIssue) ID() string {
	return i.id
}

func (i *fakeIssue) Labels() []string {
	return i.labels
}

func (i *fakeIssue) Owners() []string {
	return i.owners
}

func (i *fakeIssue) Priority() (string, bool) {
	if i.priority == "" {
		return "", false
	}
	return i.priority, true
}

func TestIssueCreator(t *testing.T) {

	i1 := &fakeIssue{
		title:    "title1",
		body:     "body<ID1>",
		id:       "<ID1>",
		labels:   []string{"kind/flake"},
		owners:   []string{},
		priority: "",
	}

	c := &fakeClient{
		t:          t,
		userName:   "BOT_USERNAME",
		dryrun:     false,
		org:        "MY_ORG",
		project:    "MY_PROJ",
		repoLabels: []string{"kind/flake", "kind/flakeypastry", "priority/P0"},
		issues: []*githubapi.Issue{
			makeTestIssue(i1.title, i1.body, "open", i1.labels, i1.owners, 0),
		},
	}
	creator := &IssueCreator{
		config: c,
	}
	if err := creator.loadCache(); err != nil {
		t.Fatalf("IssueCreator failed to load data from github while initing: %v", err)
	}

	// Test that an issue can be created normally.
	i0 := &fakeIssue{
		title:    "title0",
		body:     "body<ID0>moarbody",
		id:       "<ID0>",
		labels:   []string{"kind/flake"},
		owners:   []string{"user0"},
		priority: "",
	}
	creator.Sync(i0)
	if !c.Verify(i0.title, i0.body, i0.owners, i0.labels) {
		t.Errorf("Failed to do a simple sync of i0\n")
	}

	// Test that issues can't be double synced.
	origLen := len(c.issues)
	creator.Sync(i1)
	if len(c.issues) > origLen {
		t.Errorf("Second sync of i1 created a duplicate issue!\n")
	}
	if !c.Verify(i1.title, i1.body, i1.owners, i1.labels) {
		t.Errorf("Second sync of i1 was not idempotent.\n")
	}

	// Test that issues with empty bodies dont get synced.
	i2 := &fakeIssue{
		title:    "title2",
		body:     "", // Indicates issue should not be synced.
		id:       "<ID2>",
		labels:   []string{"kind/flake"},
		owners:   []string{"user2"},
		priority: "",
	}
	origLen = len(c.issues)
	creator.Sync(i2)
	if len(c.issues) > origLen {
		t.Errorf("Sync of i2 with empty body should not have created issue!\n")
	}

	// Test that invalid labels are not synced.
	i3 := &fakeIssue{
		title:    "title3",
		body:     "body\\@^*<ID3>\\moarbody",
		id:       "<ID3>",
		labels:   []string{"kind/flake", "label/wannabe"},
		owners:   []string{"user3"},
		priority: "",
	}
	creator.Sync(i3)
	if !c.Verify(i3.title, i3.body, i3.owners, []string{"kind/flake"}) {
		t.Errorf("Sync of i3 was invalid. The label 'label/wannabe' should not be added to the new issue.\n")
	}

	// Test that DryRun prevents issue creation.
	c.dryrun = true
	i4 := &fakeIssue{
		title:    "title4",
		body:     "<ID4>thebody",
		id:       "<ID4>",
		labels:   []string{"kind/flake"},
		owners:   []string{"user4"},
		priority: "",
	}
	origLen = len(c.issues)
	creator.Sync(i4)
	if len(c.issues) > origLen {
		t.Errorf("Sync of i4 with DryRun on should not have created issue!\n")
	}

	c.dryrun = false

	// Test that priority labels are created properly if an issue knows its priority.
	i5 := &fakeIssue{
		title:    "title5",
		body:     "<ID5>thebody",
		id:       "<ID5>",
		labels:   []string{"kind/flake", "kind/flakeypastry"},
		owners:   []string{"user5", "user1"}, // Test multiple users and labels here too.
		priority: "P0",
	}
	creator.Sync(i5)
	if !c.Verify(i5.title, i5.body, i5.owners, []string{"kind/flake", "kind/flakeypastry", "priority/P0"}) {
		t.Errorf("Sync of i5 was invalid. The labels in the created issue were incorrect.\n")
	}
}

func makeTestIssue(title, body, state string, labels, owners []string, number int) *githubapi.Issue {
	return &githubapi.Issue{
		Title:     &title,
		Body:      &body,
		State:     &state,
		Number:    &number,
		Assignees: makeUserSlice(owners),
		Labels:    makeLabelSliceNoPtr(labels),
	}
}

func makeLabelSlice(strs []string) []*githubapi.Label {
	result := make([]*githubapi.Label, len(strs))
	for i := 0; i < len(strs); i++ {
		result[i] = &githubapi.Label{Name: &strs[i]}
	}
	return result
}

func makeLabelSliceNoPtr(strs []string) []githubapi.Label {
	result := make([]githubapi.Label, len(strs))
	for i := 0; i < len(strs); i++ {
		result[i] = githubapi.Label{Name: &strs[i]}
	}
	return result
}

func makeUserSlice(strs []string) []*githubapi.User {
	result := make([]*githubapi.User, len(strs))
	for i := 0; i < len(strs); i++ {
		result[i] = &githubapi.User{Login: &strs[i]}
	}
	return result
}

func stringSlicesEqual(strs1, strs2 []string) bool {
	sort.Strings(strs1)
	sort.Strings(strs2)
	return reflect.DeepEqual(strs1, strs2)
}

func TestOwnersSIGs(t *testing.T) {
	sampleOwnerCSV = []byte(
		`name,owner,auto-assigned,sig
some test, cjwagner,0,sigarea2
some test2, cjwagner, 1, sigarea3
some test3, cjwagner, 0, sigarea4
Sysctls should support sysctls,Random-Liu,1,node
Sysctls should support unsafe sysctls which are actually whitelisted,deads2k,1,node
testname1,cjwagner ,1,sigarea
testname2,spxtr,1,sigarea
ThirdParty resources Simple Third Party creating/deleting thirdparty objects works,luxas,1,api-machinery
Upgrade cluster upgrade should maintain a functioning cluster,luxas,1,cluster-lifecycle
Upgrade master upgrade should maintain a functioning cluster,xiang90,1,cluster-lifecycle`)

	ownerlist, err := testowner.NewOwnerListFromCsv(bytes.NewReader(sampleOwnerCSV))
	if err != nil {
		t.Fatalf("Failed to init an OwnerList: %v\n", err)
	}
	c := &IssueCreator{
		owners:       ownerlist,
		maxAssignees: 3,
		maxSIGCount:  3,
	}

	cases := []struct {
		tests        []string
		owners, sigs map[string][]string
	}{
		{
			tests:  []string{"testname1"},
			owners: map[string][]string{"cjwagner": []string{"testname1"}},
			sigs:   map[string][]string{"sigarea": []string{"testname1"}},
		},
		{
			tests:  []string{"testname1", "testname2"},
			owners: map[string][]string{"cjwagner": []string{"testname1"}, "spxtr": []string{"testname2"}},
			sigs:   map[string][]string{"sigarea": []string{"testname1", "testname2"}},
		},
		{
			tests:  []string{"testname1", "testname2", "some test"},
			owners: map[string][]string{"cjwagner": []string{"testname1", "some test"}, "spxtr": []string{"testname2"}},
			sigs:   map[string][]string{"sigarea": []string{"testname1", "testname2"}, "sigarea2": []string{"some test"}},
		},
		{
			tests:  []string{"testname1", "some test", "some test2", "some_test3"},
			owners: map[string][]string{"cjwagner": []string{"testname1", "some test", "some test2"}},
			sigs:   map[string][]string{"sigarea": []string{"testname1"}, "sigarea2": []string{"some test"}, "sigarea3": []string{"some test2"}},
		},
	}
	for _, test := range cases {
		owners := c.TestsOwners(test.tests)
		sigs := c.TestsSIGs(test.tests)
		if !reflect.DeepEqual(owners, test.owners) {
			t.Errorf("Expected owners map was %v but got %v\n", test.owners, owners)
		}
		if !reflect.DeepEqual(sigs, test.sigs) {
			t.Errorf("Expected sigs map was %v but got %v\n", test.sigs, sigs)
		}

		table := c.ExplainTestAssignments(test.tests)
		for owner, testNames := range owners {
			row := fmt.Sprintf("| %s | %s |", owner, strings.Join(testNames, "; "))
			if !strings.Contains(table, row) {
				t.Errorf("Assignment explanation table is missing row: '%s'\n", row)
			}
		}
		for sig, testNames := range sigs {
			row := fmt.Sprintf("| sig/%s | %s |", sig, strings.Join(testNames, "; "))
			if !strings.Contains(table, row) {
				t.Errorf("Assignment explanation table is missing row: '%s'\n", row)
			}
		}
	}
}
