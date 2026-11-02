import type { ProjectionSnapshot } from "../api/contracts";
import type {
  DecodedCommandReconciliationCause,
  ReplicaCommitDecision,
} from "./clientViewReplica";
import { ClientViewReplica } from "./clientViewReplica";

interface TestView {
  readonly status: string;
}

const replica = new ClientViewReplica<TestView>("worker_detail", "session-1");
declare const rawSnapshot: ProjectionSnapshot<TestView>;

// @ts-expect-error Raw envelopes cannot enter the decoded Snapshot commit API.
replica.commitDecodedSnapshot(rawSnapshot);

// @ts-expect-error A reconciliation cause requires the state-private brand.
const forgedCause: DecodedCommandReconciliationCause = {
  reason: "delta_command_identity_conflict",
  key: "forged",
  trustedPosition: undefined,
  pendingBindingFingerprint: "forged",
  requestedClientCommandIds: [],
};
void forgedCause;

// @ts-expect-error Commit decisions require the state-private brand.
const forgedDecision: ReplicaCommitDecision = {
  kind: "command_reconciliation_required",
  cause: forgedCause,
};
void forgedDecision;
