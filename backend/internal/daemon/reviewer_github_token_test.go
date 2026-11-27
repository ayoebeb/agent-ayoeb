package daemon

import "testing"

func TestReviewerGitHubTokenOnlyOnWindows(t *testing.T) {
	if reviewerGitHubToken("windows") == nil {
		t.Fatal("windows reviewers need the gh login")
	}
	if reviewerGitHubToken("linux") != nil || reviewerGitHubToken("darwin") != nil {
		t.Fatal("the token must stay out of reviewer environments that do not need it")
	}
}
