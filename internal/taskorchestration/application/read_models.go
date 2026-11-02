package application

type TaskSnapshot struct {
	TaskID             string
	TenantID           string
	InitiatorUserID    string
	WorkspaceID        string
	Status             string
	AcceptanceMode     string
	WorkerSessionID    string
	CurrentWorkerRunID string
	Revision           uint64
}
