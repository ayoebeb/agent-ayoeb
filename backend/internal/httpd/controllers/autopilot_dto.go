package controllers

import "time"

// AutopilotItemIDParam is the path parameter of the parked-item and decision routes.
type AutopilotItemIDParam struct {
	EventID string `path:"eventId" description:"Parked item identifier, e.g. ape_1."`
}

// StartAutopilotRequest is the body of POST /api/v1/sessions/{sessionId}/autopilot.
type StartAutopilotRequest struct {
	Task          string `json:"task" description:"The one task to finish without the user. AO sends it again at the end of every turn until it is verifiably done."`
	MaxIterations *int   `json:"maxIterations,omitempty" description:"Most iterations before the run parks itself for the user. Omit to use the Settings default." minimum:"1" maximum:"500"`
}

// AutopilotRunResponse is one Autopilot task run.
type AutopilotRunResponse struct {
	ID                  string     `json:"id"`
	SessionID           string     `json:"sessionId"`
	ProjectID           string     `json:"projectId"`
	Task                string     `json:"task"`
	Criteria            string     `json:"criteria" description:"The \"done when ...\" list the agent drafted from the task; empty until it records it."`
	State               string     `json:"state" enum:"running,paused_usage_limit,parked,done,stopped" description:"running: AO keeps the agent on the task. paused_usage_limit: waiting for the usage limit to reset. parked: the run stopped itself and waits for the user. done: the agent declared the task done and every gate passed. stopped: the user, or the session ending, ended it."`
	EndReason           string     `json:"endReason,omitempty" enum:"user,emergency_stop,session_ended,goal_done,max_iterations,stuck,needs_input" description:"Why the run left running; absent while it runs."`
	Iteration           int        `json:"iteration" description:"Completed turns the loop has judged."`
	MaxIterations       int        `json:"maxIterations"`
	StagnantIterations  int        `json:"stagnantIterations" description:"Consecutive iterations that changed neither the workspace nor the recorded test result. Three park the run."`
	LastGap             string     `json:"lastGap" description:"What was still failing at the last judgement."`
	Evidence            string     `json:"evidence" description:"The agent's account of the tests it ran, from its last done claim."`
	CIGap               string     `json:"ciGap" description:"Why CI could not run, as the agent declared it. A recorded gap, never a pass."`
	PermissionsAllowed  int        `json:"permissionsAllowed" description:"Permission requests the policy allowed."`
	PermissionsDenied   int        `json:"permissionsDenied" description:"Permission requests the policy denied."`
	StartedAt           time.Time  `json:"startedAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	LastNudgeAt         *time.Time `json:"lastNudgeAt,omitempty"`
	EndedAt             *time.Time `json:"endedAt,omitempty"`
	OpenParkedItemCount int        `json:"openParkedItemCount" description:"Parked items the user has not handled yet."`
}

// AutopilotEventResponse is one line of the run log the user reads.
type AutopilotEventResponse struct {
	ID         string     `json:"id"`
	RunID      string     `json:"runId"`
	SessionID  string     `json:"sessionId"`
	Kind       string     `json:"kind" enum:"started,iteration,decision,parked,gate_failed,done,stopped,run_parked,paused,resumed,permission_denied,wait_diverted"`
	Iteration  int        `json:"iteration"`
	Summary    string     `json:"summary"`
	Detail     string     `json:"detail"`
	CreatedAt  time.Time  `json:"createdAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty" description:"When the user marked a parked item handled."`
	BasedOn    []string   `json:"basedOn" description:"For a decision: the past choices of the owner it followed, as short lines."`
	Confidence string     `json:"confidence,omitempty" enum:"high,medium,low" description:"For a decision: how sure the agent was that the owner would choose this."`
}

// AutopilotMergedPRResponse is a pull request that merged while the run was going.
type AutopilotMergedPRResponse struct {
	URL   string    `json:"url"`
	Title string    `json:"title"`
	At    time.Time `json:"at"`
}

// AutopilotReportResponse is the return report: what happened while the user was away.
type AutopilotReportResponse struct {
	RunID              string                      `json:"runId"`
	Task               string                      `json:"task"`
	State              string                      `json:"state" enum:"running,paused_usage_limit,parked,done,stopped"`
	EndReason          string                      `json:"endReason,omitempty" enum:"user,emergency_stop,session_ended,goal_done,max_iterations,stuck,needs_input"`
	Iterations         int                         `json:"iterations"`
	StartedAt          time.Time                   `json:"startedAt"`
	EndedAt            *time.Time                  `json:"endedAt,omitempty"`
	MergedPRs          []AutopilotMergedPRResponse `json:"mergedPrs"`
	Decisions          []AutopilotEventResponse    `json:"decisions" description:"Choices the agent made instead of asking, oldest first."`
	Parked             []AutopilotEventResponse    `json:"parked" description:"Items set aside because they needed a hard limit crossed, oldest first."`
	Gaps               []string                    `json:"gaps" description:"What the run could not close, in plain words."`
	PermissionsAllowed int                         `json:"permissionsAllowed"`
	PermissionsDenied  int                         `json:"permissionsDenied"`
}

// AutopilotViewResponse is a session's Autopilot state: its open run, or its
// latest finished one, with the log and the return report.
type AutopilotViewResponse struct {
	Run      *AutopilotRunResponse    `json:"run,omitempty" description:"Absent when the session never had a run."`
	Events   []AutopilotEventResponse `json:"events" description:"The run log, newest first."`
	Parked   []AutopilotEventResponse `json:"parked" description:"Parked items the user has not handled."`
	Warnings []string                 `json:"warnings" description:"Gaps Autopilot cannot close for this session, such as a terminal prompt it cannot answer."`
	Report   *AutopilotReportResponse `json:"report,omitempty"`
}

// ListAutopilotResponse is the body of GET /api/v1/autopilot: every open run.
type ListAutopilotResponse struct {
	Runs []AutopilotRunResponse `json:"runs"`
}

// StopAutopilotResponse reports whether the session had an open run to stop.
type StopAutopilotResponse struct {
	Stopped bool `json:"stopped"`
}

// StopAllAutopilotResponse reports how many runs the emergency stop ended.
type StopAllAutopilotResponse struct {
	Stopped int `json:"stopped"`
}

// AutopilotCriteriaRequest is the body of POST /api/v1/sessions/{sessionId}/autopilot/criteria.
type AutopilotCriteriaRequest struct {
	Criteria string `json:"criteria" description:"A short checklist that says exactly when the task is done and how it will be proved."`
}

// AutopilotDoneRequest is the body of POST /api/v1/sessions/{sessionId}/autopilot/done.
type AutopilotDoneRequest struct {
	Evidence    string `json:"evidence" description:"The test and build commands the agent ran and what they showed."`
	CICannotRun string `json:"ciCannotRun,omitempty" description:"Why CI cannot start (for example a billing limit). Recorded as a gap, never as a pass."`
}

// AutopilotDoneResponse says whether the done claim passed the gates.
type AutopilotDoneResponse struct {
	Accepted bool                 `json:"accepted" description:"True when every gate passed and the run is finished."`
	Failures []string             `json:"failures" description:"The gates that did not pass; the agent goes back to work."`
	Gaps     []string             `json:"gaps" description:"Recorded but not blocking, such as CI that cannot start."`
	Run      AutopilotRunResponse `json:"run"`
}

// AutopilotDecideRequest is the body of POST /api/v1/sessions/{sessionId}/autopilot/decisions.
type AutopilotDecideRequest struct {
	Question   string   `json:"question"`
	Choice     string   `json:"choice"`
	Why        string   `json:"why"`
	BasedOn    []string `json:"basedOn,omitempty" description:"Ids of the owner's past decisions this choice followed (from the advice), at most 5."`
	Confidence string   `json:"confidence,omitempty" enum:"high,medium,low" description:"How sure the agent is that the owner would choose this. Low means it took the safest option that can be undone."`
}

// AutopilotDecideResponse is the recorded decision.
type AutopilotDecideResponse struct {
	Event AutopilotEventResponse `json:"event"`
}

// AutopilotParkRequest is the body of POST /api/v1/sessions/{sessionId}/autopilot/parked.
type AutopilotParkRequest struct {
	Limit string `json:"limit" enum:"money,ci,main_push,data_delete,admin,messaging,credentials,other_pcs" description:"The hard limit the item would cross."`
	Item  string `json:"item" description:"What could not be done."`
	Why   string `json:"why"`
}

// AutopilotParkResponse is the parked item.
type AutopilotParkResponse struct {
	Event         AutopilotEventResponse `json:"event"`
	AlreadyParked bool                   `json:"alreadyParked" description:"True when the same item was already parked and open; the user was not told again."`
}

// ResolveAutopilotItemResponse is the parked item after the user marked it handled.
type ResolveAutopilotItemResponse struct {
	Event AutopilotEventResponse `json:"event"`
}

// UpdateAutopilotSettingsRequest sets the default iteration cap.
type UpdateAutopilotSettingsRequest struct {
	MaxIterations *int `json:"maxIterations" description:"Most iterations a run may take before it parks itself for the user." minimum:"1" maximum:"500"`
}
