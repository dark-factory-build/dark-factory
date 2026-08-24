#![cfg_attr(test, allow(dead_code))]

use std::{collections::BTreeMap, sync::Arc};

use base64::{Engine as _, engine::general_purpose};
use serde::{Deserialize, Serialize};
#[cfg(target_arch = "wasm32")]
use sha2::{Digest as _, Sha256};
use zeroize::{Zeroize as _, Zeroizing};

#[cfg(target_arch = "wasm32")]
use crate::journal::{DeliveryJournal, Operation, OperationRecord, OperationTransition};
use crate::maintainer::MAX_EXACT_INTEGER;

pub(crate) const PRIVATE_KEY_BINDING: &str = "DARK_FACTORY_MAINTAINER_PRIVATE_KEY_PKCS8";
pub(crate) const PERMISSION_REVISION_BINDING: &str = "DARK_FACTORY_MAINTAINER_PERMISSION_REVISION";
pub(crate) const REPOSITORY_BINDING: &str = "DARK_FACTORY_MAINTAINER_REPOSITORY";
pub(crate) const REPOSITORY_OWNER_ID_BINDING: &str = "DARK_FACTORY_MAINTAINER_REPOSITORY_OWNER_ID";
pub(crate) const REPOSITORY_ID_BINDING: &str = "DARK_FACTORY_MAINTAINER_REPOSITORY_ID";
pub(crate) const PERMISSION_REVISION: &str = "maintainer-operations-v1";
const GITHUB_API_VERSION: &str = "2026-03-10";
const MAX_GITHUB_RESPONSE_BYTES: usize = 64 * 1024;
/// Publication bounds. A commit is a bounded, reviewable unit of work, not a
/// bulk upload channel, and the Worker must hold every blob in memory.
const MAX_COMMIT_FILES: usize = 50;
const MAX_COMMIT_FILE_BYTES: usize = 1_000_000;
const MAX_COMMIT_TOTAL_BYTES: usize = 4_000_000;

#[derive(Clone)]
pub(crate) struct AppAuthority(Arc<Authority>);

struct Authority {
    app_id: i64,
    private_key: PrivateKey,
    repository: RepositoryName,
    repository_owner_id: i64,
    repository_id: i64,
}

struct PrivateKey(Vec<u8>);

impl Drop for PrivateKey {
    fn drop(&mut self) {
        self.0.zeroize();
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, thiserror::Error)]
pub(crate) enum Error {
    #[error("maintainer App authority configuration is invalid")]
    Configuration,
    #[error("maintainer App authority is unavailable")]
    Unavailable,
    /// GitHub answered, and its status is the answer. Endpoints report *why*
    /// they refused, and collapsing that into "unavailable" turns a
    /// determinate refusal into a reconciliation the caller cannot resolve.
    /// Which statuses a given endpoint uses to refuse is the caller's to
    /// classify, not this type's.
    #[error("github rejected the request with status {0}")]
    Rejected(u16),
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, thiserror::Error)]
pub(crate) enum OperationError {
    #[error("maintainer operation input is invalid")]
    InvalidInput,
    #[error("operation ID is already bound to a different request")]
    Conflict,
    /// GitHub refused, determinately, and nothing changed. Distinct from
    /// `Indeterminate`, which means the outcome is genuinely unknown: a
    /// refusal leaves the operation ID retryable once the precondition the
    /// refusal names actually holds. The reason rides along because an
    /// untyped outcome has now cost two diagnosis cycles (#371): "the token
    /// lacks a permission", "the queue rejected the entry", and "there is
    /// no queue" are three different retries, and without the reason the
    /// only way to tell them apart is reading this crate's source.
    #[error("github refused the operation and nothing changed: {0}")]
    Refused(RefusalReason),
    #[error("operation outcome requires reconciliation")]
    Indeterminate,
    #[error("maintainer operation authority is unavailable")]
    Unavailable,
}

/// Why GitHub refused, said with typed classifications only. GitHub's
/// error text can quote caller input, so the text never rides along -- the
/// same discipline `github_graphql` applies to its logging.
#[derive(Clone, Copy, Debug, Eq, PartialEq, thiserror::Error)]
pub(crate) enum RefusalReason {
    /// The mutation was rejected before execution, and these are the typed
    /// error classes GitHub returned at the mutation root.
    #[error("rejected before execution as {0}")]
    Rejected(RejectionKinds),
    /// The mutation answered with neither an effect nor an error.
    #[error("answered with neither an effect nor an error")]
    NoEffect,
    /// The queue read answered, and its answer carried no merge queue for
    /// the base branch -- stated as the observation, because `entries:
    /// None` is also the shape of a null repository. The decision doc
    /// calls a queueless branch unsupported and fails closed rather than
    /// falling back to a merge.
    #[error("the queue read found no merge queue on the base branch")]
    NoMergeQueue,
}

/// Which pre-execution rejection classes appeared. More than one can:
/// GitHub reports one error per problem, so the set is carried whole
/// rather than collapsed to whichever arrived first.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq)]
pub(crate) struct RejectionKinds {
    not_found: bool,
    forbidden: bool,
    unprocessable: bool,
    rate_limited: bool,
}

impl std::fmt::Display for RejectionKinds {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let mut separate = false;
        for (present, name) in [
            (self.not_found, "NOT_FOUND"),
            (self.forbidden, "FORBIDDEN"),
            (self.unprocessable, "UNPROCESSABLE"),
            (self.rate_limited, "RATE_LIMITED"),
        ] {
            if present {
                if separate {
                    formatter.write_str("+")?;
                }
                formatter.write_str(name)?;
                separate = true;
            }
        }
        if !separate {
            // No real path constructs the empty set: `classify_graphql_errors`
            // records a class for every error it accepts and refuses an empty
            // error array earlier. But the derived `Default` is
            // crate-visible, so the impossible value must at least read as
            // what it is rather than trailing off mid-sentence.
            formatter.write_str("no recorded class")?;
        }
        Ok(())
    }
}

impl From<Error> for OperationError {
    /// A bare status carries no determinate meaning at most call sites — only
    /// the operations that know which statuses their endpoint uses to refuse
    /// may read one, and they match on `Error::Rejected` before reaching here.
    fn from(_: Error) -> Self {
        Self::Unavailable
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct CreatePullRequest {
    pub(crate) operation_id: String,
    pub(crate) head: String,
    pub(crate) head_sha: String,
    pub(crate) base: String,
    pub(crate) base_sha: String,
    pub(crate) title: String,
    pub(crate) body: String,
    pub(crate) draft: bool,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct SubmitPullRequestReview {
    pub(crate) operation_id: String,
    pub(crate) pull_number: i64,
    pub(crate) head_sha: String,
    pub(crate) event: ReviewEvent,
    pub(crate) body: String,
}

/// What the reviewer concluded, which is not the same thing as which GitHub
/// review state carries it.
///
/// Rule 2 has three outcomes -- the reviewer is satisfied, the reviewer found
/// a blocking defect, or the reviewer left a note that decides nothing -- and
/// GitHub offers this App only two states to say them in. `APPROVE` is not
/// available: the App opens the pull requests it would be approving, and
/// GitHub refuses a self-approval, which is the whole reason the review became
/// a status check instead of an approval.
///
/// So `Allow` and `Comment` both post `COMMENTED`, and the verdict itself
/// rides in a line this type renders. `verdict_line` is written by the App
/// from this typed field and is refused in caller text by
/// `free_of_review_verdict`, so a caller cannot state a verdict it did not
/// ask for -- the same discipline as the operation marker, for the same
/// reason.
#[derive(Clone, Copy, Debug, Deserialize, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub(crate) enum ReviewEvent {
    Allow,
    Comment,
    RequestChanges,
}

impl ReviewEvent {
    /// The GitHub review state this verdict is recorded as.
    const fn github_state(self) -> &'static str {
        match self {
            Self::Allow | Self::Comment => "COMMENTED",
            Self::RequestChanges => "CHANGES_REQUESTED",
        }
    }

    /// The `event` GitHub's review API accepts. `ALLOW` is this App's word,
    /// not GitHub's, so it must never reach the wire.
    const fn github_event(self) -> &'static str {
        match self {
            Self::Allow | Self::Comment => "COMMENT",
            Self::RequestChanges => "REQUEST_CHANGES",
        }
    }

    /// The verdict word the required `review` check reads.
    const fn verdict(self) -> &'static str {
        match self {
            Self::Allow => "allow",
            Self::Comment => "note",
            Self::RequestChanges => "block",
        }
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct ObservePullRequestChecks {
    pub(crate) pull_number: i64,
    pub(crate) head_sha: String,
}

/// One file's content at a path, or its removal when `content_base64` is absent.
#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct FileChange {
    pub(crate) path: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub(crate) content_base64: Option<String>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct PublishCommit {
    pub(crate) operation_id: String,
    pub(crate) branch: String,
    /// The commit the branch must currently point at. Two agents racing the
    /// same branch means the second one's expectation no longer holds and it
    /// fails closed instead of clobbering the first.
    pub(crate) expected_head_sha: String,
    pub(crate) message: String,
    pub(crate) changes: Vec<FileChange>,
}

/// Add one pull request to the merge queue for its base branch.
///
/// This replaced a direct `PUT /pulls/{n}/merge` operation, which
/// `docs/development/GITHUB_APP.md` had already ruled out: "The typed merge
/// operation uses a GitHub-enforced merge queue as its sole automated path
/// ... The broker does not request Administration permission or expose direct
/// merge as a fallback." A required queue also makes GitHub refuse that
/// endpoint outright, so the operation was both non-compliant and dead.
///
/// There is no merge method here. The queue's ruleset decides it, and a
/// caller-supplied method would either be ignored or contradict the ruleset.
#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct EnqueuePullRequest {
    pub(crate) operation_id: String,
    pub(crate) pull_number: i64,
    /// `enqueuePullRequest` takes `expectedHeadOid`, so exact-head binding is
    /// enforced by the platform on the write itself rather than only by the
    /// re-read below.
    pub(crate) head_sha: String,
    /// The branch whose queue this is. The decision doc requires the bound
    /// base as well as the head: without it, a pull request that targets a
    /// different branch than the caller believes would be enqueued onto that
    /// branch's queue instead.
    pub(crate) base: String,
}

#[derive(Debug, Deserialize, Serialize)]
pub(crate) struct CommitResult {
    pub(crate) branch: String,
    pub(crate) commit_sha: String,
    pub(crate) parent_sha: String,
}

/// Deliberately carries no `position`: it changes while the entry waits, and
/// this is a durable result an idempotent replay returns verbatim, so a stored
/// "position 3" is a lie the moment anything ahead merges.
///
/// `state_when_recorded` is named for exactly what it is, and deliberately not
/// "at enqueue": reconciliation builds this result too, and it observes an
/// entry that may have moved `QUEUED -> AWAITING_CHECKS -> UNMERGEABLE` since.
/// It is worth carrying because an entry can be `UNMERGEABLE` the moment it is
/// created -- a moved base, a conflict -- and a caller told only "queued"
/// waits for a merge that is never coming. It is a durable observation, not a
/// live status, and no operation yet reports the eventual merge outcome.
#[derive(Debug, Deserialize, Serialize)]
pub(crate) struct EnqueueResult {
    pub(crate) pull_number: i64,
    pub(crate) head_sha: String,
    pub(crate) entry_id: String,
    pub(crate) state_when_recorded: String,
}

#[derive(Debug, Deserialize, Serialize)]
pub(crate) struct PullRequestResult {
    pub(crate) number: i64,
    pub(crate) url: String,
    pub(crate) head_sha: String,
    pub(crate) base_sha: String,
}

#[derive(Debug, Deserialize, Serialize)]
pub(crate) struct ReviewResult {
    pub(crate) review_id: i64,
    pub(crate) url: String,
    pub(crate) head_sha: String,
    pub(crate) state: String,
    /// `ALLOW` and `COMMENT` are both `COMMENTED` on the wire, so `state`
    /// cannot tell a reviewer which verdict it just recorded -- on the one
    /// field that now gates merges. This echoes it back.
    ///
    /// `default` is load-bearing, not tidiness. Results are stored as JSON in
    /// the durable journal and replayed with `serde_json::from_str`, so a
    /// required field added here makes every operation completed *before* this
    /// deploy fail to deserialize -- turning an idempotent replay into a
    /// permanent `Unavailable` for an operation that succeeded. The same trap
    /// applies to any future field on any result type.
    #[serde(default)]
    pub(crate) verdict: String,
}

#[derive(Debug, Deserialize, Serialize)]
pub(crate) struct ChecksResult {
    pub(crate) pull_number: i64,
    pub(crate) head_sha: String,
    pub(crate) checks: Vec<CheckResult>,
}

#[derive(Debug, Deserialize, Serialize)]
pub(crate) struct CheckResult {
    pub(crate) name: String,
    pub(crate) status: String,
    pub(crate) conclusion: Option<String>,
    pub(crate) url: String,
}

impl AppAuthority {
    pub(crate) fn new(
        app_id: i64,
        private_key: String,
        permission_revision: String,
        repository: String,
        repository_owner_id: String,
        repository_id: String,
    ) -> Result<Self, Error> {
        if permission_revision != PERMISSION_REVISION {
            return Err(Error::Configuration);
        }
        let private_key = Zeroizing::new(private_key);
        let private_key = general_purpose::STANDARD
            .decode(private_key.as_bytes())
            .map_err(|_| Error::Configuration)?;
        if !(1_000..=16_384).contains(&private_key.len()) {
            return Err(Error::Configuration);
        }
        let repository = RepositoryName::new(repository)?;
        let repository_owner_id = exact_integer(&repository_owner_id)?;
        let repository_id = exact_integer(&repository_id)?;
        Ok(Self(Arc::new(Authority {
            app_id,
            private_key: PrivateKey(private_key),
            repository,
            repository_owner_id,
            repository_id,
        })))
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn verify(&self) -> Result<(), Error> {
        self.0.verify().await
    }

    pub(crate) fn repository(&self) -> &str {
        &self.0.repository.full_name
    }

    pub(crate) fn repository_id(&self) -> i64 {
        self.0.repository_id
    }

    pub(crate) const fn permission_revision(&self) -> &'static str {
        PERMISSION_REVISION
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn create_pull_request(
        &self,
        journal: &DeliveryJournal,
        request: CreatePullRequest,
    ) -> Result<PullRequestResult, OperationError> {
        request.validate()?;
        let operation = request.operation("create_pull_request")?;
        let state = journal
            .begin_operation(&operation)
            .await
            .map_err(|_| OperationError::Unavailable)?;
        if let Some(result) = completed_or_conflict::<PullRequestResult>(&state)? {
            return Ok(result);
        }
        let token = self
            .0
            .installation_token(BTreeMap::from([
                ("contents", "read"),
                ("metadata", "read"),
                ("pull_requests", "write"),
            ]))
            .await?;
        if let Some(result) = self.0.reconcile_pull_request(&token, &request).await? {
            return complete(journal, &operation, result).await;
        }
        if matches!(
            state,
            OperationRecord::Executing | OperationRecord::Indeterminate
        ) {
            journal
                .mark_operation(&operation, OperationTransition::Indeterminate)
                .await
                .map_err(|_| OperationError::Unavailable)?;
            return Err(OperationError::Indeterminate);
        }
        self.0
            .verify_ref(&token, &request.head, &request.head_sha)
            .await?;
        self.0
            .verify_ref(&token, &request.base, &request.base_sha)
            .await?;
        match journal
            .mark_operation(&operation, OperationTransition::Executing)
            .await
            .map_err(|_| OperationError::Unavailable)?
        {
            OperationRecord::Claimed => {}
            OperationRecord::Completed(result) => {
                return serde_json::from_str(&result).map_err(|_| OperationError::Unavailable);
            }
            OperationRecord::Conflict => return Err(OperationError::Conflict),
            OperationRecord::Executing | OperationRecord::Indeterminate => {
                if let Some(result) = self.0.reconcile_pull_request(&token, &request).await? {
                    return complete(journal, &operation, result).await;
                }
                return Err(OperationError::Indeterminate);
            }
            OperationRecord::New | OperationRecord::Planned => {
                return Err(OperationError::Unavailable);
            }
        }
        match self.0.post_pull_request(&token, &request).await {
            Ok(result) => complete(journal, &operation, result).await,
            Err(_) => {
                if let Some(result) = self.0.reconcile_pull_request(&token, &request).await? {
                    return complete(journal, &operation, result).await;
                }
                let _ = journal
                    .mark_operation(&operation, OperationTransition::Indeterminate)
                    .await;
                Err(OperationError::Indeterminate)
            }
        }
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn submit_pull_request_review(
        &self,
        journal: &DeliveryJournal,
        request: SubmitPullRequestReview,
    ) -> Result<ReviewResult, OperationError> {
        request.validate()?;
        let operation = request.operation("submit_pull_request_review")?;
        let state = journal
            .begin_operation(&operation)
            .await
            .map_err(|_| OperationError::Unavailable)?;
        if let Some(result) = completed_or_conflict::<ReviewResult>(&state)? {
            return Ok(result);
        }
        let token = self
            .0
            .installation_token(BTreeMap::from([
                ("contents", "read"),
                ("metadata", "read"),
                ("pull_requests", "write"),
            ]))
            .await?;
        self.0
            .verify_pull_request_head(&token, request.pull_number, &request.head_sha)
            .await?;
        if let Some(result) = self.0.reconcile_review(&token, &request).await? {
            return complete(journal, &operation, result).await;
        }
        if matches!(
            state,
            OperationRecord::Executing | OperationRecord::Indeterminate
        ) {
            journal
                .mark_operation(&operation, OperationTransition::Indeterminate)
                .await
                .map_err(|_| OperationError::Unavailable)?;
            return Err(OperationError::Indeterminate);
        }
        match journal
            .mark_operation(&operation, OperationTransition::Executing)
            .await
            .map_err(|_| OperationError::Unavailable)?
        {
            OperationRecord::Claimed => {}
            OperationRecord::Completed(result) => {
                return serde_json::from_str(&result).map_err(|_| OperationError::Unavailable);
            }
            OperationRecord::Conflict => return Err(OperationError::Conflict),
            OperationRecord::Executing | OperationRecord::Indeterminate => {
                if let Some(result) = self.0.reconcile_review(&token, &request).await? {
                    return complete(journal, &operation, result).await;
                }
                return Err(OperationError::Indeterminate);
            }
            OperationRecord::New | OperationRecord::Planned => {
                return Err(OperationError::Unavailable);
            }
        }
        match self.0.post_review(&token, &request).await {
            Ok(result) => complete(journal, &operation, result).await,
            Err(_) => {
                if let Some(result) = self.0.reconcile_review(&token, &request).await? {
                    return complete(journal, &operation, result).await;
                }
                let _ = journal
                    .mark_operation(&operation, OperationTransition::Indeterminate)
                    .await;
                Err(OperationError::Indeterminate)
            }
        }
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn publish_commit(
        &self,
        journal: &DeliveryJournal,
        request: PublishCommit,
    ) -> Result<CommitResult, OperationError> {
        request.validate()?;
        let operation = request.operation("publish_commit")?;
        let state = journal
            .begin_operation(&operation)
            .await
            .map_err(|_| OperationError::Unavailable)?;
        if let Some(result) = completed_or_conflict::<CommitResult>(&state)? {
            return Ok(result);
        }
        let token = self
            .0
            .installation_token(BTreeMap::from([
                ("contents", "write"),
                ("metadata", "read"),
            ]))
            .await?;
        if let Some(result) = self
            .0
            .reconcile_commit(&token, &request)
            .await
            .map_err(|_| OperationError::Indeterminate)?
        {
            return complete(journal, &operation, result).await;
        }
        if matches!(
            state,
            OperationRecord::Executing | OperationRecord::Indeterminate
        ) {
            journal
                .mark_operation(&operation, OperationTransition::Indeterminate)
                .await
                .map_err(|_| OperationError::Unavailable)?;
            return Err(OperationError::Indeterminate);
        }
        // `verify_publish_precondition` already reports a moved head as a
        // conflict. Rewriting every other failure into one too told the caller
        // to refetch a head that had not moved.
        let branch_exists = self.0.verify_publish_precondition(&token, &request).await?;
        match journal
            .mark_operation(&operation, OperationTransition::Executing)
            .await
            .map_err(|_| OperationError::Unavailable)?
        {
            OperationRecord::Claimed => {}
            OperationRecord::Completed(result) => {
                return serde_json::from_str(&result).map_err(|_| OperationError::Unavailable);
            }
            OperationRecord::Conflict => return Err(OperationError::Conflict),
            OperationRecord::Executing | OperationRecord::Indeterminate => {
                if let Some(result) = self.0.reconcile_commit(&token, &request).await? {
                    return complete(journal, &operation, result).await;
                }
                return Err(OperationError::Indeterminate);
            }
            OperationRecord::New | OperationRecord::Planned => {
                return Err(OperationError::Unavailable);
            }
        }
        match self.0.push_commit(&token, &request, branch_exists).await {
            Ok(result) => complete(journal, &operation, result).await,
            Err(_) => {
                if let Some(result) = self.0.reconcile_commit(&token, &request).await? {
                    return complete(journal, &operation, result).await;
                }
                let _ = journal
                    .mark_operation(&operation, OperationTransition::Indeterminate)
                    .await;
                Err(OperationError::Indeterminate)
            }
        }
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn enqueue_pull_request(
        &self,
        journal: &DeliveryJournal,
        request: EnqueuePullRequest,
    ) -> Result<EnqueueResult, OperationError> {
        request.validate()?;
        let operation = request.operation("enqueue_pull_request")?;
        let state = journal
            .begin_operation(&operation)
            .await
            .map_err(|_| OperationError::Unavailable)?;
        if let Some(result) = completed_or_conflict::<EnqueueResult>(&state)? {
            return Ok(result);
        }
        let token = self
            .0
            .installation_token(BTreeMap::from([
                ("merge_queues", "write"),
                ("metadata", "read"),
                // Enqueueing mutates the pull request's queue state. This
                // minted `read` once, and the first live enqueue was refused
                // wholesale (#371); every sibling operation that mutates a
                // pull request mints `write`, and the reviewed permission
                // revision already grants it.
                ("pull_requests", "write"),
            ]))
            .await?;
        if let Some(result) = self.0.reconcile_enqueue(&token, &request).await? {
            return complete(journal, &operation, result).await;
        }
        if matches!(
            state,
            OperationRecord::Executing | OperationRecord::Indeterminate
        ) {
            journal
                .mark_operation(&operation, OperationTransition::Indeterminate)
                .await
                .map_err(|_| OperationError::Unavailable)?;
            return Err(OperationError::Indeterminate);
        }
        // The decision doc requires the bound base and head re-read before
        // enqueueing, not only bound on the write.
        let pull = self
            .0
            .verify_pull_request_head(&token, request.pull_number, &request.head_sha)
            .await?;
        if pull.base.name != request.base {
            return Err(OperationError::Conflict);
        }
        match journal
            .mark_operation(&operation, OperationTransition::Executing)
            .await
            .map_err(|_| OperationError::Unavailable)?
        {
            OperationRecord::Claimed => {}
            OperationRecord::Completed(result) => {
                return serde_json::from_str(&result).map_err(|_| OperationError::Unavailable);
            }
            OperationRecord::Conflict => return Err(OperationError::Conflict),
            OperationRecord::Executing | OperationRecord::Indeterminate => {
                if let Some(result) = self.0.reconcile_enqueue(&token, &request).await? {
                    return complete(journal, &operation, result).await;
                }
                return Err(OperationError::Indeterminate);
            }
            OperationRecord::New | OperationRecord::Planned => {
                return Err(OperationError::Unavailable);
            }
        }
        match self.0.enqueue_entry(&token, &pull.node_id, &request).await {
            Ok(result) => complete(journal, &operation, result).await,
            Err(OperationError::Refused(reason)) => {
                // Nothing was enqueued, and we know it. Release the claim so
                // this same operation ID and request can be retried once the
                // pull request is actually queueable; burying it at
                // `indeterminate` would force a fresh UUID for every retry and
                // would report an unknown outcome for a fully known one.
                journal
                    .mark_operation(&operation, OperationTransition::Refused)
                    .await
                    .map_err(|_| OperationError::Unavailable)?;
                Err(OperationError::Refused(reason))
            }
            Err(_) => {
                if let Some(result) = self.0.reconcile_enqueue(&token, &request).await? {
                    return complete(journal, &operation, result).await;
                }
                let _ = journal
                    .mark_operation(&operation, OperationTransition::Indeterminate)
                    .await;
                Err(OperationError::Indeterminate)
            }
        }
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn observe_pull_request_checks(
        &self,
        request: ObservePullRequestChecks,
    ) -> Result<ChecksResult, OperationError> {
        request.validate()?;
        let token = self
            .0
            .installation_token(BTreeMap::from([
                ("checks", "read"),
                ("contents", "read"),
                ("metadata", "read"),
                ("pull_requests", "read"),
            ]))
            .await?;
        self.0
            .verify_pull_request_head(&token, request.pull_number, &request.head_sha)
            .await?;
        self.0.checks(&token, request).await
    }
}

impl CreatePullRequest {
    fn validate(&self) -> Result<(), OperationError> {
        valid_operation_id(&self.operation_id)?;
        valid_ref(&self.head)?;
        valid_ref(&self.base)?;
        valid_sha(&self.head_sha)?;
        valid_sha(&self.base_sha)?;
        valid_text(&self.title, 1, 256, false)?;
        valid_text(&self.body, 0, 30_000, true)?;
        free_of_operation_marker(&self.body)?;
        if self.head == self.base || self.head_sha == self.base_sha {
            return Err(OperationError::InvalidInput);
        }
        Ok(())
    }

    #[cfg(target_arch = "wasm32")]
    fn operation(&self, kind: &str) -> Result<Operation, OperationError> {
        operation(kind, &self.operation_id, self)
    }

    fn marker(&self) -> String {
        format!("{OPERATION_MARKER_PREFIX}{} -->", self.operation_id)
    }

    fn marked_body(&self) -> String {
        if self.body.is_empty() {
            self.marker()
        } else {
            format!("{}\n\n{}", self.body, self.marker())
        }
    }
}

impl SubmitPullRequestReview {
    fn validate(&self) -> Result<(), OperationError> {
        valid_operation_id(&self.operation_id)?;
        valid_exact_integer(self.pull_number)?;
        valid_sha(&self.head_sha)?;
        valid_text(&self.body, 1, 16_000, true)?;
        free_of_operation_marker(&self.body)?;
        free_of_review_verdict(&self.body)
    }

    #[cfg(target_arch = "wasm32")]
    fn operation(&self, kind: &str) -> Result<Operation, OperationError> {
        operation(kind, &self.operation_id, self)
    }

    fn marker(&self) -> String {
        format!("{OPERATION_MARKER_PREFIX}{} -->", self.operation_id)
    }

    /// The reviewer's own text, then the App-rendered verdict, then the
    /// operation marker.
    ///
    /// The head SHA is repeated in the verdict line for a human reading the
    /// thread. It is not what the check trusts: GitHub records `commit_id`
    /// from the App's request, so that field is the binding, and the two
    /// cannot disagree because both are rendered from `head_sha` here.
    fn marked_body(&self) -> String {
        format!(
            "{}\n\n{REVIEW_VERDICT_PREFIX} {} {}\n{}",
            self.body,
            self.event.verdict(),
            self.head_sha,
            self.marker()
        )
    }
}

impl PublishCommit {
    fn validate(&self) -> Result<(), OperationError> {
        valid_operation_id(&self.operation_id)?;
        valid_ref(&self.branch)?;
        valid_sha(&self.expected_head_sha)?;
        valid_text(&self.message, 1, 4_096, true)?;
        free_of_operation_marker(&self.message)?;
        if !(1..=MAX_COMMIT_FILES).contains(&self.changes.len()) {
            return Err(OperationError::InvalidInput);
        }
        let mut total = 0_usize;
        for (index, change) in self.changes.iter().enumerate() {
            valid_repository_path(&change.path)?;
            // A commit that touched the same path twice would leave which write
            // wins up to GitHub's tree ordering.
            if self.changes[..index]
                .iter()
                .any(|earlier| earlier.path == change.path)
            {
                return Err(OperationError::InvalidInput);
            }
            if let Some(content) = change.content_base64.as_deref() {
                if content.len() > MAX_COMMIT_FILE_BYTES {
                    return Err(OperationError::InvalidInput);
                }
                general_purpose::STANDARD
                    .decode(content)
                    .map_err(|_| OperationError::InvalidInput)?;
                total = total
                    .checked_add(content.len())
                    .ok_or(OperationError::InvalidInput)?;
            }
        }
        (total <= MAX_COMMIT_TOTAL_BYTES)
            .then_some(())
            .ok_or(OperationError::InvalidInput)
    }

    #[cfg(target_arch = "wasm32")]
    fn operation(&self, kind: &str) -> Result<Operation, OperationError> {
        operation(kind, &self.operation_id, self)
    }

    fn trailer(&self) -> String {
        format!("{OPERATION_TRAILER_PREFIX} {}", self.operation_id)
    }

    /// The trailer makes a landed commit self-identifying, so a retry after an
    /// indeterminate failure can tell "already published" from "not published"
    /// by reading the branch rather than guessing.
    fn marked_message(&self) -> String {
        format!("{}\n\n{}", self.message, self.trailer())
    }
}

impl EnqueuePullRequest {
    fn validate(&self) -> Result<(), OperationError> {
        valid_operation_id(&self.operation_id)?;
        valid_exact_integer(self.pull_number)?;
        valid_sha(&self.head_sha)?;
        valid_ref(&self.base)
    }

    #[cfg(target_arch = "wasm32")]
    fn operation(&self, kind: &str) -> Result<Operation, OperationError> {
        operation(kind, &self.operation_id, self)
    }
}

impl ObservePullRequestChecks {
    fn validate(&self) -> Result<(), OperationError> {
        valid_exact_integer(self.pull_number)?;
        valid_sha(&self.head_sha)
    }
}

fn valid_operation_id(value: &str) -> Result<(), OperationError> {
    let valid = value.len() == 36
        && [8, 13, 18, 23]
            .into_iter()
            .all(|index| value.as_bytes().get(index) == Some(&b'-'))
        && value
            .bytes()
            .enumerate()
            .all(|(index, byte)| [8, 13, 18, 23].contains(&index) || byte.is_ascii_hexdigit())
        && value == value.to_ascii_lowercase();
    valid.then_some(()).ok_or(OperationError::InvalidInput)
}

/// A reconciler identifies its own work by a marker the caller does not get to
/// write: `publish_commit` puts `Dark-Factory-Operation: <id>` in the commit
/// message, and the pull-request operations put an HTML comment in the body.
/// Both are matched with `contains`, so caller text carrying either prefix
/// could name a *different* operation's id and make that operation's
/// reconciliation adopt work it never did. Refuse the prefix at the door
/// rather than trying to out-parse it afterwards.
const OPERATION_TRAILER_PREFIX: &str = "Dark-Factory-Operation:";
const OPERATION_MARKER_PREFIX: &str = "<!-- dark-factory-operation:";

fn free_of_operation_marker(value: &str) -> Result<(), OperationError> {
    let forged =
        value.contains(OPERATION_TRAILER_PREFIX) || value.contains(OPERATION_MARKER_PREFIX);
    (!forged).then_some(()).ok_or(OperationError::InvalidInput)
}

/// The line the required `review` check reads to decide whether rule 2 is
/// satisfied at an exact head. `ReviewEvent::verdict` renders it; review
/// bodies are refused if they carry the prefix, for the same reason the
/// operation marker is refused: a caller that could write this line could
/// state a verdict it never asked the App to record, and the check would
/// believe it.
///
/// Only review bodies are constrained. A commit message or pull request body
/// mentioning the prefix is harmless -- the check reads reviews and nothing
/// else -- and refusing it there would stop the documentation describing the
/// format from being publishable.
const REVIEW_VERDICT_PREFIX: &str = "Dark-Factory-Review:";

fn free_of_review_verdict(value: &str) -> Result<(), OperationError> {
    (!value.contains(REVIEW_VERDICT_PREFIX))
        .then_some(())
        .ok_or(OperationError::InvalidInput)
}

/// A path inside the repository, as a commit may address it.
///
/// The `.github` authority tree is refused explicitly. GitHub already blocks
/// `.github/workflows/` without the `workflows` permission, which this App
/// deliberately does not hold, but a policy-checked surface should state the
/// boundary rather than depend on a permission staying un-granted: an agent
/// that could rewrite the CI gating its own work would be escalating its
/// authority.
///
/// The refusal is by path segment, not by prefix. A tree entry may name a
/// directory as a blob, which replaces the whole subtree, so `.github` and
/// `.github/workflows` must be refused as paths in their own right and not
/// only as prefixes of a file inside them.
///
/// The review and dependency policy files are refused by name for the same
/// reason. Refusing the `.github` tree stops an agent destroying them
/// wholesale but not rewriting one of them in place, and an agent that can
/// edit CODEOWNERS can remove itself from required review — the same
/// escalation, reached one file at a time. GitHub reads CODEOWNERS from the
/// repository root, `.github/`, and `docs/`, so all three are named.
///
/// Each of those names is also refused as a leading directory. A tree entry
/// at `.github/CODEOWNERS/x` turns the CODEOWNERS blob into a tree, deleting
/// its content as a side effect: exactly the write the by-name refusal
/// exists to prevent, reached by making the name a directory instead. The
/// collision is with the *leading* segments only, so a path that merely
/// contains a protected name -- `CODEOWNERS.md`, `docs/notes-on-CODEOWNERS.md`
/// -- stays publishable. The `.github` tree needs no such rule: its refusal
/// already covers everything beneath it.
fn valid_repository_path(value: &str) -> Result<(), OperationError> {
    const REVIEW_AUTHORITY_PATHS: &[&str] = &[
        "CODEOWNERS",
        ".github/CODEOWNERS",
        "docs/CODEOWNERS",
        ".github/dependabot.yml",
        ".github/dependabot.yaml",
    ];

    let mut segments = value.split('/');
    let github_authority = segments
        .next()
        .is_some_and(|segment| segment.eq_ignore_ascii_case(".github"))
        && segments
            .next()
            .is_none_or(|segment| segment.eq_ignore_ascii_case("workflows"));
    let review_authority = REVIEW_AUTHORITY_PATHS.iter().any(|&protected| {
        value
            .strip_prefix(protected)
            .is_some_and(|rest| rest.is_empty() || rest.starts_with('/'))
    });
    let valid = !value.is_empty()
        && value.len() <= 240
        && !value.starts_with('/')
        && !value.ends_with('/')
        && !value.contains("//")
        && !value.split('/').any(|segment| {
            segment.is_empty() || segment == "." || segment == ".." || segment == ".git"
        })
        && value
            .chars()
            .all(|character| !character.is_control() && character != '\\')
        && !github_authority
        && !review_authority;
    valid.then_some(()).ok_or(OperationError::InvalidInput)
}

fn valid_ref(value: &str) -> Result<(), OperationError> {
    let valid = !value.is_empty()
        && value.len() <= 240
        && !value.starts_with('/')
        && !value.ends_with('/')
        && !value.ends_with('.')
        && !value.contains("..")
        && !value.contains("//")
        && !value.contains("@{")
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.' | b'/'));
    valid.then_some(()).ok_or(OperationError::InvalidInput)
}

fn valid_sha(value: &str) -> Result<(), OperationError> {
    (value.len() == 40
        && value.bytes().all(|byte| byte.is_ascii_hexdigit())
        && value == value.to_ascii_lowercase())
    .then_some(())
    .ok_or(OperationError::InvalidInput)
}

fn valid_exact_integer(value: i64) -> Result<(), OperationError> {
    (1..=MAX_EXACT_INTEGER)
        .contains(&value)
        .then_some(())
        .ok_or(OperationError::InvalidInput)
}

fn valid_text(
    value: &str,
    min: usize,
    max: usize,
    allow_newline: bool,
) -> Result<(), OperationError> {
    let len = value.len();
    let valid = (min..=max).contains(&len)
        && value.chars().all(|character| {
            !character.is_control() || (allow_newline && matches!(character, '\n' | '\r' | '\t'))
        });
    valid.then_some(()).ok_or(OperationError::InvalidInput)
}

#[cfg(target_arch = "wasm32")]
fn operation<T: Serialize>(
    kind: &str,
    operation_id: &str,
    request: &T,
) -> Result<Operation, OperationError> {
    let canonical = serde_json::to_vec(request).map_err(|_| OperationError::InvalidInput)?;
    Ok(Operation {
        operation_id: operation_id.to_owned(),
        kind: kind.to_owned(),
        request_digest: hex::encode(Sha256::digest(canonical)),
    })
}

#[cfg(target_arch = "wasm32")]
fn completed_or_conflict<T: serde::de::DeserializeOwned>(
    state: &OperationRecord,
) -> Result<Option<T>, OperationError> {
    match state {
        OperationRecord::Completed(result) => serde_json::from_str(result)
            .map(Some)
            .map_err(|_| OperationError::Unavailable),
        OperationRecord::Conflict => Err(OperationError::Conflict),
        _ => Ok(None),
    }
}

#[cfg(target_arch = "wasm32")]
async fn complete<T: Serialize + serde::de::DeserializeOwned>(
    journal: &DeliveryJournal,
    operation: &Operation,
    result: T,
) -> Result<T, OperationError> {
    let result_json = serde_json::to_string(&result).map_err(|_| OperationError::Unavailable)?;
    match journal
        .mark_operation(operation, OperationTransition::Completed(result_json))
        .await
        .map_err(|_| OperationError::Unavailable)?
    {
        OperationRecord::Completed(stored) => {
            serde_json::from_str(&stored).map_err(|_| OperationError::Unavailable)
        }
        OperationRecord::Conflict => Err(OperationError::Conflict),
        _ => Err(OperationError::Unavailable),
    }
}

fn exact_integer(value: &str) -> Result<i64, Error> {
    value
        .parse::<i64>()
        .ok()
        .filter(|value| (1..=MAX_EXACT_INTEGER).contains(value))
        .ok_or(Error::Configuration)
}

struct RepositoryName {
    full_name: String,
    owner: String,
    name: String,
}

impl RepositoryName {
    fn new(value: String) -> Result<Self, Error> {
        let Some((owner, name)) = value.split_once('/') else {
            return Err(Error::Configuration);
        };
        if value.matches('/').count() != 1
            || !valid_path_segment(owner, 39, false)
            || !valid_path_segment(name, 100, true)
        {
            return Err(Error::Configuration);
        }
        let owner = owner.to_owned();
        let name = name.to_owned();
        Ok(Self {
            full_name: value,
            owner,
            name,
        })
    }

    fn installation_url(&self) -> String {
        format!(
            "https://api.github.com/repos/{}/{}/installation",
            self.owner, self.name
        )
    }
}

fn valid_path_segment(value: &str, max: usize, allow_dot_underscore: bool) -> bool {
    !value.is_empty()
        && value.len() <= max
        && value != "."
        && value != ".."
        && value.bytes().all(|byte| {
            byte.is_ascii_alphanumeric()
                || byte == b'-'
                || (allow_dot_underscore && (byte == b'.' || byte == b'_'))
        })
}

fn jwt_unsigned(app_id: i64, now: i64) -> String {
    let header = general_purpose::URL_SAFE_NO_PAD.encode(br#"{"alg":"RS256","typ":"JWT"}"#);
    let claims = format!(
        r#"{{"iat":{},"exp":{},"iss":"{}"}}"#,
        now - 60,
        now + 540,
        app_id
    );
    format!(
        "{header}.{}",
        general_purpose::URL_SAFE_NO_PAD.encode(claims)
    )
}

#[cfg(target_arch = "wasm32")]
impl Authority {
    async fn verify(&self) -> Result<(), Error> {
        // An App JWT authenticates only App-level endpoints, so repository identity
        // cannot be read here. Prove the key signs, the App and owner match, and the
        // installation is the exact selected-repository one — and mint nothing. This
        // endpoint is reachable unauthenticated through /readyz, so issuing a
        // credential here would let a stranger exhaust the App's rate limit and
        // disable every real operation. The repository_id <-> full_name <-> owner_id
        // binding is enforced by `installation_token` on each actual operation, which
        // is the correct enforcement point.
        let jwt = self.jwt().await?;
        let installation: Installation =
            github_json_as_app(&self.repository.installation_url(), jwt.as_str()).await?;
        validate_installation(&installation, self.app_id, self.repository_owner_id)
    }

    async fn jwt(&self) -> Result<Credential, Error> {
        let now = (js_sys::Date::now() / 1_000.0).floor() as i64;
        let unsigned = jwt_unsigned(self.app_id, now);
        let signature = sign_rs256(&self.private_key.0, unsigned.as_bytes())
            .await
            .inspect_err(|_| worker::console_error!("app jwt signing failed"))?;
        Credential::new(format!(
            "{unsigned}.{}",
            general_purpose::URL_SAFE_NO_PAD.encode(signature)
        ))
    }

    async fn installation_token(
        &self,
        permissions: BTreeMap<&'static str, &'static str>,
    ) -> Result<Credential, OperationError> {
        let jwt = self.jwt().await?;
        let installation: Installation =
            github_json_as_app(&self.repository.installation_url(), jwt.as_str()).await?;
        validate_installation(&installation, self.app_id, self.repository_owner_id)?;
        #[derive(Serialize)]
        struct TokenRequest {
            repository_ids: [i64; 1],
            permissions: BTreeMap<&'static str, &'static str>,
        }
        let expected_permissions = permissions
            .iter()
            .map(|(name, level)| ((*name).to_owned(), (*level).to_owned()))
            .collect::<BTreeMap<_, _>>();
        let token_url = format!(
            "https://api.github.com/app/installations/{}/access_tokens",
            installation.id
        );
        if !app_jwt_endpoint(&token_url) {
            worker::console_error!("refusing to present an app jwt to {token_url}");
            return Err(OperationError::Unavailable);
        }
        let response: InstallationToken = github_json_request(
            worker::Method::Post,
            &token_url,
            jwt.as_str(),
            Some(&TokenRequest {
                repository_ids: [self.repository_id],
                permissions,
            }),
        )
        .await?;
        if response.permissions != expected_permissions
            || response.repositories.as_slice()
                != [RepositoryIdentity {
                    id: self.repository_id,
                    full_name: self.repository.full_name.clone(),
                    owner: NumericIdentity {
                        id: self.repository_owner_id,
                    },
                }]
        {
            worker::console_error!(
                "installation token contract mismatch: {} permission(s), {} repository(ies)",
                response.permissions.len(),
                response.repositories.len()
            );
            return Err(OperationError::Unavailable);
        }
        Credential::new(response.token).map_err(Into::into)
    }

    /// Blobs, then a tree over the base commit's tree, then a commit, then a
    /// non-forced ref update. The ref update is the only mutation that matters:
    /// GitHub rejects it if the branch moved since `expected_head_sha`, so a
    /// racing agent loses rather than overwrites.
    /// Either the branch is at exactly `expected_head_sha`, or it does not exist
    /// yet and `expected_head_sha` is the commit it will start from. Returns
    /// whether the branch already exists.
    async fn verify_publish_precondition(
        &self,
        token: &Credential,
        request: &PublishCommit,
    ) -> Result<bool, OperationError> {
        // Read the ref directly rather than reusing `verify_ref`, which cannot
        // distinguish "branch is somewhere else" from "branch does not exist".
        // Conflating them turned a stale head into an attempted branch create,
        // which failed late and reported indeterminate instead of conflict.
        match github_json::<GitReference>(
            &format!(
                "https://api.github.com/repos/{}/{}/git/ref/heads/{}",
                self.repository.owner,
                self.repository.name,
                percent_encode(&request.branch)
            ),
            token.as_str(),
        )
        .await
        {
            Ok(reference) => (reference.object.kind == "commit"
                && reference.object.sha == request.expected_head_sha)
                .then_some(true)
                .ok_or(OperationError::Conflict),
            // Only a 404 means the branch is absent. Reading any other failure
            // as absence sent a publish onto a branch sitting exactly at
            // `expected_head_sha` down the create path, where `POST /git/refs`
            // answers "Reference already exists" and wedges the operation at
            // indeterminate for a branch that never moved.
            Err(Error::Rejected(404)) => {
                // The parent must still be a real commit, so a typo cannot
                // create a branch from nothing.
                let _: GitCommit = github_json(
                    &format!(
                        "https://api.github.com/repos/{}/{}/git/commits/{}",
                        self.repository.owner, self.repository.name, request.expected_head_sha
                    ),
                    token.as_str(),
                )
                .await?;
                Ok(false)
            }
            Err(error) => Err(error.into()),
        }
    }

    async fn push_commit(
        &self,
        token: &Credential,
        request: &PublishCommit,
        branch_exists: bool,
    ) -> Result<CommitResult, OperationError> {
        let base: GitCommit = github_json(
            &format!(
                "https://api.github.com/repos/{}/{}/git/commits/{}",
                self.repository.owner, self.repository.name, request.expected_head_sha
            ),
            token.as_str(),
        )
        .await?;
        let mut entries = Vec::with_capacity(request.changes.len());
        for change in &request.changes {
            let sha = match change.content_base64.as_deref() {
                Some(content) => {
                    let blob: GitObjectId = github_json_request(
                        worker::Method::Post,
                        &format!(
                            "https://api.github.com/repos/{}/{}/git/blobs",
                            self.repository.owner, self.repository.name
                        ),
                        token.as_str(),
                        Some(&BlobRequest {
                            content,
                            encoding: "base64",
                        }),
                    )
                    .await?;
                    Some(blob.sha)
                }
                // A null sha in a tree entry deletes the path.
                None => None,
            };
            entries.push(TreeEntry {
                path: change.path.clone(),
                mode: "100644",
                kind: "blob",
                sha,
            });
        }
        let tree: GitObjectId = github_json_request(
            worker::Method::Post,
            &format!(
                "https://api.github.com/repos/{}/{}/git/trees",
                self.repository.owner, self.repository.name
            ),
            token.as_str(),
            Some(&TreeRequest {
                base_tree: &base.tree.sha,
                tree: &entries,
            }),
        )
        .await?;
        let commit: GitObjectId = github_json_request(
            worker::Method::Post,
            &format!(
                "https://api.github.com/repos/{}/{}/git/commits",
                self.repository.owner, self.repository.name
            ),
            token.as_str(),
            Some(&CommitRequest {
                message: request.marked_message(),
                tree: &tree.sha,
                parents: [&request.expected_head_sha],
            }),
        )
        .await?;
        // Creating a ref fails if it already exists and a non-forced update
        // fails if the branch moved, so neither path can clobber another agent.
        let updated: GitReference = if branch_exists {
            github_json_request(
                worker::Method::Patch,
                &format!(
                    "https://api.github.com/repos/{}/{}/git/refs/heads/{}",
                    self.repository.owner,
                    self.repository.name,
                    percent_encode(&request.branch)
                ),
                token.as_str(),
                Some(&RefUpdate {
                    sha: &commit.sha,
                    force: false,
                }),
            )
            .await?
        } else {
            github_json_request(
                worker::Method::Post,
                &format!(
                    "https://api.github.com/repos/{}/{}/git/refs",
                    self.repository.owner, self.repository.name
                ),
                token.as_str(),
                Some(&RefCreate {
                    reference: &format!("refs/heads/{}", request.branch),
                    sha: &commit.sha,
                }),
            )
            .await?
        };
        (updated.object.sha == commit.sha)
            .then_some(CommitResult {
                branch: request.branch.clone(),
                commit_sha: commit.sha,
                parent_sha: request.expected_head_sha.clone(),
            })
            .ok_or(OperationError::Indeterminate)
    }

    /// Did this exact operation already land? The trailer makes the commit
    /// self-identifying, so a retry after an indeterminate failure reads the
    /// branch instead of guessing.
    async fn reconcile_commit(
        &self,
        token: &Credential,
        request: &PublishCommit,
    ) -> Result<Option<CommitResult>, OperationError> {
        let reference: GitReference = match github_json(
            &format!(
                "https://api.github.com/repos/{}/{}/git/ref/heads/{}",
                self.repository.owner,
                self.repository.name,
                percent_encode(&request.branch)
            ),
            token.as_str(),
        )
        .await
        {
            Ok(reference) => reference,
            Err(_) => return Ok(None),
        };
        if reference.object.sha == request.expected_head_sha {
            return Ok(None);
        }
        let head: GitCommit = github_json(
            &format!(
                "https://api.github.com/repos/{}/{}/git/commits/{}",
                self.repository.owner, self.repository.name, reference.object.sha
            ),
            token.as_str(),
        )
        .await?;
        // The trailer alone is not proof. It travels with the message through a
        // rebase or a cherry-pick, and `validate` is the only thing stopping a
        // caller writing another operation's trailer into its own commit, so
        // the tip must also still be a direct child of the stated head. That is
        // what makes the reported `parent_sha` true rather than assumed.
        if !head.message.contains(&request.trailer())
            || !matches!(head.parents.as_slice(), [parent] if parent.sha == request.expected_head_sha)
        {
            // The branch moved for some other reason; this operation did not
            // land and must not claim it did.
            return Ok(None);
        }
        Ok(Some(CommitResult {
            branch: request.branch.clone(),
            commit_sha: reference.object.sha,
            parent_sha: request.expected_head_sha.clone(),
        }))
    }

    /// Enqueue via GraphQL, which is the only API GitHub offers for this.
    ///
    /// `expectedHeadOid` is a first-class input, so the head binding is
    /// enforced by the write itself. `jump` is never sent: the decision doc
    /// forbids queue-jump authority, and omitting the field is a stronger
    /// guarantee than sending `false`.
    async fn enqueue_entry(
        &self,
        token: &Credential,
        pull_node_id: &str,
        request: &EnqueuePullRequest,
    ) -> Result<EnqueueResult, OperationError> {
        #[derive(Serialize)]
        struct Variables<'a> {
            pull: &'a str,
            head: &'a str,
        }
        #[derive(Deserialize)]
        struct Data {
            #[serde(rename = "enqueuePullRequest")]
            enqueue: Option<Payload>,
        }
        #[derive(Deserialize)]
        struct Payload {
            #[serde(rename = "mergeQueueEntry")]
            entry: Option<QueueEntry>,
        }

        let (data, failure): (Option<Data>, Option<GraphQlFailure>) = github_graphql(
            token,
            "mutation($pull:ID!,$head:GitObjectID!){\
             enqueuePullRequest(input:{pullRequestId:$pull,expectedHeadOid:$head}){\
             mergeQueueEntry{id state pullRequest{number headRefOid}}}}",
            &Variables {
                pull: pull_node_id,
                head: &request.head_sha,
            },
        )
        .await?;
        let entry = enqueue_outcome(
            data.and_then(|data| data.enqueue)
                .and_then(|payload| payload.entry),
            failure,
        )?;
        entry.into_result(request)
    }

    /// Answer "is this pull request queued at the head I stated?" by reading
    /// GitHub, never a local record.
    ///
    /// Unlike every other reconciler here, this one has no operation marker to
    /// match on: a queue entry carries no field the App can write. So it can
    /// adopt an entry some *other* actor created -- a second operation, or an
    /// operator who enqueued by hand. That is accepted rather than solved,
    /// because the effect is idempotent by construction: "pull request N is
    /// queued at head H" is the whole of what this operation produces, and it
    /// is equally true whoever brought it about.
    async fn reconcile_enqueue(
        &self,
        token: &Credential,
        request: &EnqueuePullRequest,
    ) -> Result<Option<EnqueueResult>, OperationError> {
        #[derive(Serialize)]
        struct Variables<'a> {
            owner: &'a str,
            name: &'a str,
            base: &'a str,
        }
        #[derive(Deserialize)]
        struct Data {
            repository: Option<Repository>,
        }
        #[derive(Deserialize)]
        struct Repository {
            #[serde(rename = "mergeQueue")]
            queue: Option<Queue>,
        }
        #[derive(Deserialize)]
        struct Queue {
            entries: Option<Entries>,
        }
        #[derive(Deserialize)]
        struct Entries {
            #[serde(rename = "totalCount")]
            total_count: i64,
            nodes: Vec<QueueEntry>,
        }

        let (data, failure): (Option<Data>, Option<GraphQlFailure>) = github_graphql(
            token,
            "query($owner:String!,$name:String!,$base:String!){\
             repository(owner:$owner,name:$name){\
             mergeQueue(branch:$base){entries(first:100){totalCount \
             nodes{id state pullRequest{number headRefOid}}}}}}",
            &Variables {
                owner: &self.repository.owner,
                name: &self.repository.name,
                base: &request.base,
            },
        )
        .await?;
        // A read that carried ANY error did not answer, whether or not `data`
        // came back partially populated. Reporting a refusal here would tell
        // the caller "nothing changed" about an enqueue whose outcome is
        // exactly what could not be read -- and this runs on the path where
        // the mutation's outcome is already unknown.
        if failure.is_some() {
            return Err(OperationError::Indeterminate);
        }
        let Some(data) = data else {
            return Err(OperationError::Indeterminate);
        };
        let Some(entries) = data
            .repository
            .and_then(|repository| repository.queue)
            .and_then(|queue| queue.entries)
        else {
            // The read answered, and its answer carried no queue for this
            // branch. The decision doc calls that unsupported and fails
            // closed rather than falling back.
            return Err(OperationError::Refused(RefusalReason::NoMergeQueue));
        };
        // One unread page could hide this operation's entry and make a
        // reconciliation answer "not queued" for something that is. Refuse to
        // guess instead.
        // `nodes.len() == totalCount` is the proof that the whole queue was
        // read, so the page size does not need naming twice: a short page
        // fails this comparison exactly as an over-long queue would.
        if entries.nodes.len() as i64 != entries.total_count {
            return Err(OperationError::Indeterminate);
        }
        entries
            .nodes
            .into_iter()
            .find(|entry| entry.matches(request))
            .map(|entry| entry.into_result(request))
            .transpose()
    }

    async fn verify_ref(
        &self,
        token: &Credential,
        name: &str,
        expected_sha: &str,
    ) -> Result<(), OperationError> {
        let reference: GitReference = github_json(
            &format!(
                "https://api.github.com/repos/{}/{}/git/ref/heads/{}",
                self.repository.owner,
                self.repository.name,
                percent_encode(name)
            ),
            token.as_str(),
        )
        .await?;
        (reference.name == format!("refs/heads/{name}")
            && reference.object.kind == "commit"
            && reference.object.sha == expected_sha)
            .then_some(())
            .ok_or(OperationError::Conflict)
    }

    async fn verify_pull_request_head(
        &self,
        token: &Credential,
        pull_number: i64,
        expected_sha: &str,
    ) -> Result<PullRequest, OperationError> {
        let pull: PullRequest = github_json(
            &format!(
                "https://api.github.com/repos/{}/{}/pulls/{pull_number}",
                self.repository.owner, self.repository.name
            ),
            token.as_str(),
        )
        .await?;
        if pull.number != pull_number || pull.head.sha != expected_sha {
            return Err(OperationError::Conflict);
        }
        Ok(pull)
    }

    async fn reconcile_pull_request(
        &self,
        token: &Credential,
        request: &CreatePullRequest,
    ) -> Result<Option<PullRequestResult>, OperationError> {
        let pulls: Vec<PullRequest> = github_json(
            &format!(
                "https://api.github.com/repos/{}/{}/pulls?state=all&head={}%3A{}&base={}&per_page=100",
                self.repository.owner,
                self.repository.name,
                percent_encode(&self.repository.owner),
                percent_encode(&request.head),
                percent_encode(&request.base)
            ),
            token.as_str(),
        )
        .await?;
        let matches = pulls
            .into_iter()
            .filter(|pull| {
                pull.body
                    .as_deref()
                    .is_some_and(|body| body.contains(&request.marker()))
                    && pull.head.name == request.head
                    && pull.head.sha == request.head_sha
                    && pull.base.name == request.base
                    && pull.base.sha == request.base_sha
            })
            .map(PullRequestResult::try_from)
            .collect::<Result<Vec<_>, _>>()?;
        match matches.as_slice() {
            [] => Ok(None),
            [result] => Ok(Some(PullRequestResult {
                number: result.number,
                url: result.url.clone(),
                head_sha: result.head_sha.clone(),
                base_sha: result.base_sha.clone(),
            })),
            _ => Err(OperationError::Indeterminate),
        }
    }

    async fn post_pull_request(
        &self,
        token: &Credential,
        request: &CreatePullRequest,
    ) -> Result<PullRequestResult, OperationError> {
        #[derive(Serialize)]
        struct Body<'a> {
            title: &'a str,
            head: &'a str,
            base: &'a str,
            body: String,
            draft: bool,
        }
        let pull: PullRequest = github_json_request(
            worker::Method::Post,
            &format!(
                "https://api.github.com/repos/{}/{}/pulls",
                self.repository.owner, self.repository.name
            ),
            token.as_str(),
            Some(&Body {
                title: &request.title,
                head: &request.head,
                base: &request.base,
                body: request.marked_body(),
                draft: request.draft,
            }),
        )
        .await?;
        let result = PullRequestResult::try_from(pull)?;
        if result.head_sha != request.head_sha || result.base_sha != request.base_sha {
            return Err(OperationError::Indeterminate);
        }
        Ok(result)
    }

    async fn reconcile_review(
        &self,
        token: &Credential,
        request: &SubmitPullRequestReview,
    ) -> Result<Option<ReviewResult>, OperationError> {
        let reviews: Vec<PullRequestReview> = github_json(
            &format!(
                "https://api.github.com/repos/{}/{}/pulls/{}/reviews?per_page=100",
                self.repository.owner, self.repository.name, request.pull_number
            ),
            token.as_str(),
        )
        .await?;
        let mut matches = reviews
            .into_iter()
            .filter(|review| review.matches(request))
            .map(|review| review.into_result(request))
            .collect::<Result<Vec<_>, _>>()?;
        match matches.len() {
            0 => Ok(None),
            1 => Ok(matches.pop()),
            // Two reviews carrying one operation's marker at one head is not
            // something to pick a winner from.
            _ => Err(OperationError::Indeterminate),
        }
    }

    async fn post_review(
        &self,
        token: &Credential,
        request: &SubmitPullRequestReview,
    ) -> Result<ReviewResult, OperationError> {
        #[derive(Serialize)]
        struct Body<'a> {
            commit_id: &'a str,
            body: String,
            event: &'a str,
        }
        let review: PullRequestReview = github_json_request(
            worker::Method::Post,
            &format!(
                "https://api.github.com/repos/{}/{}/pulls/{}/reviews",
                self.repository.owner, self.repository.name, request.pull_number
            ),
            token.as_str(),
            Some(&Body {
                commit_id: &request.head_sha,
                body: request.marked_body(),
                event: request.event.github_event(),
            }),
        )
        .await?;
        let result = review.into_result(request)?;
        if result.head_sha != request.head_sha || result.state != request.event.github_state() {
            return Err(OperationError::Indeterminate);
        }
        Ok(result)
    }

    async fn checks(
        &self,
        token: &Credential,
        request: ObservePullRequestChecks,
    ) -> Result<ChecksResult, OperationError> {
        let response: CheckRuns = github_json(
            &format!(
                "https://api.github.com/repos/{}/{}/commits/{}/check-runs?per_page=100",
                self.repository.owner, self.repository.name, request.head_sha
            ),
            token.as_str(),
        )
        .await?;
        if !(0..=100).contains(&response.total_count)
            || response.total_count as usize != response.check_runs.len()
        {
            return Err(OperationError::Unavailable);
        }
        let mut checks = response
            .check_runs
            .into_iter()
            .map(CheckResult::try_from)
            .collect::<Result<Vec<_>, _>>()?;
        checks.sort_by(|left, right| left.name.cmp(&right.name));
        Ok(ChecksResult {
            pull_number: request.pull_number,
            head_sha: request.head_sha,
            checks,
        })
    }
}

struct Credential(String);

impl Credential {
    fn new(token: String) -> Result<Self, Error> {
        if token.len() < 20 || token.len() > 4_096 || !token.is_ascii() {
            return Err(Error::Unavailable);
        }
        Ok(Self(token))
    }

    fn as_str(&self) -> &str {
        &self.0
    }
}

impl Drop for Credential {
    fn drop(&mut self) {
        self.0.zeroize();
    }
}

#[derive(Deserialize)]
struct Installation {
    id: i64,
    app_id: i64,
    account: NumericIdentity,
    repository_selection: String,
    permissions: BTreeMap<String, String>,
    events: Vec<String>,
    suspended_at: Option<serde_json::Value>,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq)]
struct NumericIdentity {
    id: i64,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq)]
struct RepositoryIdentity {
    id: i64,
    full_name: String,
    owner: NumericIdentity,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct InstallationToken {
    token: String,
    permissions: BTreeMap<String, String>,
    repositories: Vec<RepositoryIdentity>,
}

#[cfg(target_arch = "wasm32")]
#[derive(Serialize)]
struct BlobRequest<'a> {
    content: &'a str,
    encoding: &'static str,
}

#[cfg(target_arch = "wasm32")]
#[derive(Serialize)]
struct TreeEntry {
    path: String,
    mode: &'static str,
    #[serde(rename = "type")]
    kind: &'static str,
    // Serialised as null when absent, which is how a tree entry deletes a path.
    sha: Option<String>,
}

#[cfg(target_arch = "wasm32")]
#[derive(Serialize)]
struct TreeRequest<'a> {
    base_tree: &'a str,
    tree: &'a [TreeEntry],
}

#[cfg(target_arch = "wasm32")]
#[derive(Serialize)]
struct CommitRequest<'a> {
    message: String,
    tree: &'a str,
    parents: [&'a str; 1],
}

#[cfg(target_arch = "wasm32")]
#[derive(Serialize)]
struct RefUpdate<'a> {
    sha: &'a str,
    force: bool,
}

#[cfg(target_arch = "wasm32")]
#[derive(Serialize)]
struct RefCreate<'a> {
    #[serde(rename = "ref")]
    reference: &'a str,
    sha: &'a str,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct GitObjectId {
    sha: String,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct GitCommit {
    message: String,
    tree: GitObjectId,
    parents: Vec<GitParent>,
}

/// A parent entry carries no `type`, so it cannot reuse `GitObject`.
#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct GitParent {
    sha: String,
}

#[cfg(any(target_arch = "wasm32", test))]
#[derive(Deserialize)]
struct QueueEntry {
    id: String,
    state: String,
    #[serde(rename = "pullRequest")]
    pull_request: Option<QueueEntryPullRequest>,
}

#[cfg(any(target_arch = "wasm32", test))]
#[derive(Deserialize)]
struct QueueEntryPullRequest {
    number: i64,
    #[serde(rename = "headRefOid")]
    head_ref_oid: String,
}

#[cfg(any(target_arch = "wasm32", test))]
impl QueueEntry {
    /// The entry's own `headCommit` is the queue's *synthetic* merge commit,
    /// not the pull request head -- verified live against a real queue entry,
    /// where they differed. Matching on it would never find the head the
    /// caller stated. `pullRequest.headRefOid` is the live head, and GitHub
    /// ejects an entry when the head moves, so a surviving entry names the
    /// head it was queued at.
    fn matches(&self, request: &EnqueuePullRequest) -> bool {
        self.pull_request.as_ref().is_some_and(|pull| {
            pull.number == request.pull_number && pull.head_ref_oid == request.head_sha
        })
    }

    fn into_result(self, request: &EnqueuePullRequest) -> Result<EnqueueResult, OperationError> {
        if !self.matches(request) {
            // GitHub answered about a different pull request or head than the
            // one asked about. Nothing safe can be concluded from that.
            return Err(OperationError::Indeterminate);
        }
        valid_text(&self.id, 1, 256, true)?;
        // GitHub's documented `MergeQueueEntryState`. An unknown value means
        // the schema moved under us, and guessing would report a merge state
        // this build does not understand.
        if !matches!(
            self.state.as_str(),
            "QUEUED" | "AWAITING_CHECKS" | "MERGEABLE" | "UNMERGEABLE" | "LOCKED"
        ) {
            return Err(OperationError::Indeterminate);
        }
        Ok(EnqueueResult {
            pull_number: request.pull_number,
            head_sha: request.head_sha.clone(),
            entry_id: self.id,
            state_when_recorded: self.state,
        })
    }
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct GitReference {
    #[serde(rename = "ref")]
    name: String,
    object: GitObject,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct GitObject {
    sha: String,
    #[serde(rename = "type")]
    kind: String,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct PullRequest {
    number: i64,
    node_id: String,
    html_url: String,
    body: Option<String>,
    head: PullReference,
    base: PullReference,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct PullReference {
    #[serde(rename = "ref")]
    name: String,
    sha: String,
}

#[cfg(target_arch = "wasm32")]
impl TryFrom<PullRequest> for PullRequestResult {
    type Error = OperationError;

    fn try_from(pull: PullRequest) -> Result<Self, Self::Error> {
        valid_exact_integer(pull.number)?;
        valid_github_url(&pull.html_url)?;
        valid_sha(&pull.head.sha)?;
        valid_sha(&pull.base.sha)?;
        Ok(Self {
            number: pull.number,
            url: pull.html_url,
            head_sha: pull.head.sha,
            base_sha: pull.base.sha,
        })
    }
}

#[cfg(any(target_arch = "wasm32", test))]
#[derive(Deserialize)]
struct PullRequestReview {
    id: i64,
    html_url: String,
    body: Option<String>,
    commit_id: String,
    state: String,
}

#[cfg(any(target_arch = "wasm32", test))]
impl PullRequestReview {
    fn matches(&self, request: &SubmitPullRequestReview) -> bool {
        self.body
            .as_deref()
            .is_some_and(|body| body.contains(&request.marker()))
            && self.commit_id == request.head_sha
            && self.state == request.event.github_state()
    }
}

#[cfg(any(target_arch = "wasm32", test))]
impl PullRequestReview {
    /// The verdict comes from the request, not from GitHub: `ALLOW` and
    /// `COMMENT` are indistinguishable in the review's `state`, and the App is
    /// the only thing that knows which one it rendered.
    fn into_result(
        self,
        request: &SubmitPullRequestReview,
    ) -> Result<ReviewResult, OperationError> {
        valid_exact_integer(self.id)?;
        valid_github_url(&self.html_url)?;
        valid_sha(&self.commit_id)?;
        if !matches!(self.state.as_str(), "COMMENTED" | "CHANGES_REQUESTED") {
            return Err(OperationError::Unavailable);
        }
        Ok(ReviewResult {
            review_id: self.id,
            url: self.html_url,
            head_sha: self.commit_id,
            state: self.state,
            verdict: request.event.verdict().into(),
        })
    }
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct CheckRuns {
    total_count: i64,
    check_runs: Vec<CheckRun>,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct CheckRun {
    name: String,
    status: String,
    conclusion: Option<String>,
    html_url: String,
}

#[cfg(target_arch = "wasm32")]
impl TryFrom<CheckRun> for CheckResult {
    type Error = OperationError;

    fn try_from(check: CheckRun) -> Result<Self, Self::Error> {
        valid_text(&check.name, 1, 256, false)?;
        if !matches!(
            check.status.as_str(),
            "queued" | "in_progress" | "completed" | "pending" | "waiting" | "requested"
        ) || !check.conclusion.as_deref().is_none_or(|conclusion| {
            matches!(
                conclusion,
                "action_required"
                    | "cancelled"
                    | "failure"
                    | "neutral"
                    | "skipped"
                    | "stale"
                    | "startup_failure"
                    | "success"
                    | "timed_out"
            )
        }) {
            return Err(OperationError::Unavailable);
        }
        valid_github_url(&check.html_url)?;
        Ok(Self {
            name: check.name,
            status: check.status,
            conclusion: check.conclusion,
            url: check.html_url,
        })
    }
}

#[cfg(any(target_arch = "wasm32", test))]
fn valid_github_url(value: &str) -> Result<(), OperationError> {
    (value.starts_with("https://github.com/")
        && value.len() <= 2_048
        && value.is_ascii()
        && !value.contains(['\r', '\n']))
    .then_some(())
    .ok_or(OperationError::Unavailable)
}

#[cfg(target_arch = "wasm32")]
fn percent_encode(value: &str) -> String {
    const HEX: &[u8; 16] = b"0123456789ABCDEF";
    let mut encoded = String::with_capacity(value.len());
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.' | b'~') {
            encoded.push(char::from(byte));
        } else {
            encoded.push('%');
            encoded.push(char::from(HEX[(byte >> 4) as usize]));
            encoded.push(char::from(HEX[(byte & 0x0f) as usize]));
        }
    }
    encoded
}

fn validate_installation(
    installation: &Installation,
    app_id: i64,
    owner_id: i64,
) -> Result<(), Error> {
    let rejected: Vec<&str> = [
        (installation.id <= 0).then_some("id"),
        (installation.app_id != app_id).then_some("app_id"),
        (installation.account.id != owner_id).then_some("account_id"),
        (installation.repository_selection != "selected").then_some("repository_selection"),
        (!permission_at_least(&installation.permissions, "checks", "read")).then_some("checks"),
        // `publish_commit` mints `contents: write`. Accepting a read-only
        // installation let readiness pass and then failed at token mint, where
        // GitHub's 422 reaches the caller as an opaque "authority is
        // unavailable".
        (!permission_at_least(&installation.permissions, "contents", "write"))
            .then_some("contents"),
        (!permission_at_least(&installation.permissions, "metadata", "read")).then_some("metadata"),
        (!permission_at_least(&installation.permissions, "pull_requests", "write"))
            .then_some("pull_requests"),
        // `enqueue_pull_request` mints `merge_queues: write`, and it is the
        // only automated path to the default branch. Omitting it here is the
        // same fail-open the `contents` line above exists to prevent: an
        // installation without Merge queues would pass `/readyz`, report
        // authority ready, and then fail every enqueue at token mint. This
        // check is also what makes readiness the answer to "was the permission
        // actually granted?" rather than something nothing records.
        (!permission_at_least(&installation.permissions, "merge_queues", "write"))
            .then_some("merge_queues"),
        (!installation.events.is_empty()).then_some("events"),
        (installation.suspended_at.is_some()).then_some("suspended_at"),
    ]
    .into_iter()
    .flatten()
    .collect();
    if !rejected.is_empty() {
        #[cfg(target_arch = "wasm32")]
        worker::console_error!("installation rejected on: {}", rejected.join(","));
        return Err(Error::Unavailable);
    }
    Ok(())
}

fn permission_at_least(permissions: &BTreeMap<String, String>, name: &str, required: &str) -> bool {
    matches!(
        (permissions.get(name).map(String::as_str), required),
        (Some("read" | "write" | "admin"), "read")
            | (Some("write" | "admin"), "write")
            | (Some("admin"), "admin")
    )
}

/// A GitHub App JWT authenticates only App-level endpoints. Every other REST
/// resource requires an installation token, and GitHub answers `Bad
/// credentials` when a JWT is presented to one.
#[cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]
fn app_jwt_endpoint(url: &str) -> bool {
    // Match the path only. A query or fragment can otherwise supply the suffix
    // (`.../pulls?x=/installation`), and dot segments are collapsed by the URL
    // parser after this check runs.
    let path = &url[..url.find(['?', '#']).unwrap_or(url.len())];
    if path.contains("..") {
        return false;
    }
    path == "https://api.github.com/app"
        || path.starts_with("https://api.github.com/app/installations/")
        || (path.starts_with("https://api.github.com/repos/") && path.ends_with("/installation"))
}

#[cfg(target_arch = "wasm32")]
async fn github_json_as_app<T: serde::de::DeserializeOwned>(
    url: &str,
    jwt: &str,
) -> Result<T, Error> {
    if !app_jwt_endpoint(url) {
        worker::console_error!("refusing to present an app jwt to {url}");
        return Err(Error::Unavailable);
    }
    github_json(url, jwt).await
}

#[cfg(any(target_arch = "wasm32", test))]
#[derive(Deserialize)]
struct GraphQlError {
    #[serde(rename = "type")]
    kind: Option<String>,
    /// The response path the error was raised at. A path deeper than the
    /// mutation root means a resolver had to run to produce an object to
    /// select into, so the effect may already exist -- whatever the type says.
    #[serde(default)]
    path: Vec<serde_json::Value>,
}

/// Classify a GraphQL `errors` array into what it establishes about the
/// operation, or `None` when there were no errors.
///
/// Pure, so it is host-testable: the transport around it is `wasm32`-only, and
/// this table is where "the operation did not run" is asserted. Getting a
/// class wrong in the permissive direction reports "nothing changed" for
/// something that may have changed.
///
/// `RATE_LIMITED` is rejected-before-execution because GitHub refuses the
/// request rather than half-running it. `SERVICE_UNAVAILABLE` and every
/// untyped error are not: GitHub returns an untyped `Something went wrong
/// while executing your query` for what its own documentation says "may be the
/// result of a timeout", and a request that timed out server-side may have
/// executed.
#[cfg(any(target_arch = "wasm32", test))]
fn classify_graphql_errors(errors: &[GraphQlError]) -> Option<GraphQlFailure> {
    if errors.is_empty() {
        return None;
    }
    let mut kinds = RejectionKinds::default();
    for error in errors {
        // A type alone is not enough. GitHub raises typed errors -- including
        // `FORBIDDEN`, for permission scoping on an installation token -- while
        // *resolving* deep fields, and an error at a path below the mutation
        // root is post-execution by construction: the resolver had to run to
        // produce something to select into. On this mutation that matters,
        // because `mergeQueueEntry.id` and `.state` are non-null, so an error
        // on either nulls `mergeQueueEntry` itself and the entry looks absent
        // for an enqueue that already happened.
        //
        // A root-level error keeps its type: the SAML `FORBIDDEN` GitHub
        // documents arrives at `path: ["<mutationField>"]`, length one, and is
        // a genuine pre-execution rejection.
        if error.path.len() > 1 {
            return Some(GraphQlFailure::Unknown);
        }
        match error.kind.as_deref() {
            Some("NOT_FOUND") => kinds.not_found = true,
            Some("FORBIDDEN") => kinds.forbidden = true,
            Some("UNPROCESSABLE") => kinds.unprocessable = true,
            Some("RATE_LIMITED") => kinds.rate_limited = true,
            _ => return Some(GraphQlFailure::Unknown),
        }
    }
    Some(GraphQlFailure::Rejected(kinds))
}

/// What an enqueue mutation established, decided from the **payload** rather
/// than the envelope.
///
/// This is the distinction the first version got wrong. A GraphQL field error
/// nulls the field it names and leaves `data` an object, so GitHub's ordinary
/// refusal shape is a populated `data` carrying `enqueuePullRequest: null`
/// *beside* an errors array. Asking "did the envelope have data?" routes every
/// real refusal past the classification and answers `Refused` for outcomes
/// nobody knows.
///
/// Split out so it is host-testable: the transport is `wasm32`-only, and a
/// grep-style assertion on the source cannot catch a rewrite of this decision.
#[cfg(any(target_arch = "wasm32", test))]
fn enqueue_outcome(
    entry: Option<QueueEntry>,
    failure: Option<GraphQlFailure>,
) -> Result<QueueEntry, OperationError> {
    match (entry, failure) {
        // An entry came back. That is the effect, errors alongside it or not.
        (Some(entry), _) => Ok(entry),
        // Rejected before execution: nothing was queued, and the same
        // operation ID stays retryable once the precondition holds.
        (None, Some(GraphQlFailure::Rejected(kinds))) => {
            Err(OperationError::Refused(RefusalReason::Rejected(kinds)))
        }
        // An untyped error may follow a server-side timeout on work already
        // under way. The caller reconciles against the queue rather than
        // asserting nothing happened.
        (None, Some(GraphQlFailure::Unknown)) => Err(OperationError::Indeterminate),
        // No entry and no error is a determinate "did not enqueue".
        (None, None) => Err(OperationError::Refused(RefusalReason::NoEffect)),
    }
}

/// What a GraphQL response establishes, handed back to the caller rather than
/// decided here.
///
/// A mutation and a read need opposite answers to the same response, so the
/// transport must not collapse them: for `enqueue_entry` a rejected request
/// means "nothing happened, retry this operation ID"; for `reconcile_enqueue`
/// the identical response means "I could not find out", which is never a
/// refusal of the operation being reconciled.
#[cfg(any(target_arch = "wasm32", test))]
#[derive(Clone, Copy, Debug)]
enum GraphQlFailure {
    /// Every error names a class GitHub rejects *before* running the
    /// operation, so no effect was produced. Which classes appeared rides
    /// along, because the reason ends at the MCP boundary (#371).
    Rejected(RejectionKinds),
    /// At least one error carries no type, or one GitHub can return after it
    /// began executing. Whether the effect landed is not knowable from this.
    Unknown,
}

/// One typed GraphQL operation, not a proxy: each query text is a
/// compile-time constant and callers supply only typed variables. There is no
/// path by which an agent's input becomes GraphQL.
///
/// GraphQL answers **200 with an `errors` array** for a rejected operation, so
/// a status check alone reports success for a failure. Two consequences are
/// handled here rather than assumed away:
///
/// - `data` and `errors` can both be populated. That partial result is what
///   GitHub actually did, so it is returned rather than discarded -- a
///   field-level error under the selection would otherwise be reported as
///   "nothing changed" for an operation that landed.
/// - Not every error means the operation did not run. GitHub returns 200 with
///   an untyped `Something went wrong while executing your query` for what its
///   own documentation says "may be the result of a timeout", and a request
///   that timed out server-side may have executed. Only error classes that are
///   rejected before execution are reported as such; anything else is unknown,
///   which fails safe.
#[cfg(target_arch = "wasm32")]
async fn github_graphql<T: serde::de::DeserializeOwned, V: Serialize>(
    token: &Credential,
    query: &str,
    variables: &V,
) -> Result<(Option<T>, Option<GraphQlFailure>), Error> {
    #[derive(Serialize)]
    struct Body<'a, V> {
        query: &'a str,
        variables: &'a V,
    }
    #[derive(Deserialize)]
    struct Envelope<T> {
        data: Option<T>,
        #[serde(default)]
        errors: Vec<GraphQlError>,
    }
    let envelope: Envelope<T> = github_json_request(
        worker::Method::Post,
        "https://api.github.com/graphql",
        token.as_str(),
        Some(&Body { query, variables }),
    )
    .await?;
    // Diagnosis only, and unconditional: a field error nulls its field and
    // leaves `data` an object, so returning early on a populated `data` would
    // mean the most common refusal logged nothing at all. Messages can quote
    // caller input, so only the typed classification is logged, never the text.
    for error in &envelope.errors {
        worker::console_error!(
            "github graphql error: {}",
            error.kind.as_deref().unwrap_or("untyped")
        );
    }
    Ok((envelope.data, classify_graphql_errors(&envelope.errors)))
}

#[cfg(target_arch = "wasm32")]
async fn github_json<T: serde::de::DeserializeOwned>(
    url: &str,
    credential: &str,
) -> Result<T, Error> {
    github_json_request::<T, serde_json::Value>(worker::Method::Get, url, credential, None).await
}

#[cfg(target_arch = "wasm32")]
async fn github_json_request<T: serde::de::DeserializeOwned, B: Serialize>(
    method: worker::Method,
    url: &str,
    credential: &str,
    body: Option<&B>,
) -> Result<T, Error> {
    use futures_util::TryStreamExt as _;
    use worker::{Fetch, Headers, Request, RequestInit, RequestRedirect};

    let headers = Headers::new();
    let authorization = Zeroizing::new(format!("Bearer {credential}"));
    // Set the credential outside the logged loop. Nothing below formats a header
    // value, and keeping the credential out of that iteration means a later edit to
    // the log line cannot put a live token into Cloudflare logs.
    headers
        .set("authorization", authorization.as_str())
        .map_err(|_| Error::Unavailable)?;
    for (name, value) in [
        ("accept", "application/vnd.github+json"),
        ("user-agent", "dark-factory-control-plane/0.1"),
        ("x-github-api-version", GITHUB_API_VERSION),
    ] {
        headers.set(name, value).map_err(|error| {
            worker::console_error!("github header {name} rejected: {error}");
            Error::Unavailable
        })?;
    }
    let body = body
        .map(serde_json::to_string)
        .transpose()
        .map_err(|_| Error::Unavailable)?;
    if body.is_some() {
        headers
            .set("content-type", "application/json")
            .map_err(|_| Error::Unavailable)?;
    }
    let mut init = RequestInit::new();
    init.with_method(method)
        // Workers' Request supports only `follow` and `manual`; `error` makes the
        // constructor throw, so no request ever leaves the edge. `manual` returns
        // the redirect itself, which the status check below rejects as non-2xx —
        // a redirect is still never followed.
        .with_redirect(RequestRedirect::Manual)
        .with_headers(headers)
        .with_body(body.map(Into::into));
    let request = Request::new_with_init(url, &init).map_err(|error| {
        worker::console_error!("github request could not be built for {url}: {error}");
        Error::Unavailable
    })?;
    // Diagnosis only: never log credentials, headers, or bodies.
    let mut response = match Fetch::Request(request).send().await {
        Ok(response) => response,
        Err(error) => {
            worker::console_error!("github request failed: {url}: {error:?}");
            return Err(Error::Unavailable);
        }
    };
    if !(200..300).contains(&response.status_code()) {
        worker::console_error!(
            "github rejected {url} with status {}",
            response.status_code()
        );
        return Err(Error::Rejected(response.status_code()));
    }
    let mut stream = response.stream().map_err(|_| Error::Unavailable)?;
    let mut bytes = Vec::new();
    while let Some(mut chunk) = stream.try_next().await.map_err(|_| Error::Unavailable)? {
        let next_len = bytes
            .len()
            .checked_add(chunk.len())
            .ok_or(Error::Unavailable)?;
        if next_len > MAX_GITHUB_RESPONSE_BYTES {
            return Err(Error::Unavailable);
        }
        bytes.append(&mut chunk);
    }
    serde_json::from_slice(&bytes).map_err(|error| {
        worker::console_error!(
            "github response from {url} did not match the expected shape ({} bytes): {error}",
            bytes.len()
        );
        Error::Unavailable
    })
}

#[cfg(target_arch = "wasm32")]
async fn sign_rs256(private_key: &[u8], message: &[u8]) -> Result<Vec<u8>, Error> {
    use js_sys::{Array, Function, Object, Promise, Reflect, Uint8Array};
    use wasm_bindgen::{JsCast as _, JsValue};
    use wasm_bindgen_futures::JsFuture;

    let algorithm = Object::new();
    Reflect::set(&algorithm, &"name".into(), &"RSASSA-PKCS1-v1_5".into())
        .map_err(|_| Error::Unavailable)?;
    Reflect::set(&algorithm, &"hash".into(), &"SHA-256".into()).map_err(|_| Error::Unavailable)?;
    let global = js_sys::global();
    let crypto = Reflect::get(&global, &"crypto".into()).map_err(|_| Error::Unavailable)?;
    let subtle = Reflect::get(&crypto, &"subtle".into()).map_err(|_| Error::Unavailable)?;
    let import_key: Function = Reflect::get(&subtle, &"importKey".into())
        .map_err(|_| Error::Unavailable)?
        .dyn_into()
        .map_err(|_| Error::Unavailable)?;
    let usages = Array::new();
    usages.push(&"sign".into());
    let key_bytes = Uint8Array::from(private_key);
    let promise: Promise = import_key
        .call5(
            &subtle,
            &"pkcs8".into(),
            key_bytes.as_ref(),
            algorithm.as_ref(),
            &JsValue::FALSE,
            usages.as_ref(),
        )
        .map_err(|_| Error::Unavailable)?
        .dyn_into()
        .map_err(|_| Error::Unavailable)?;
    let key = JsFuture::from(promise)
        .await
        .map_err(|_| Error::Unavailable)?;
    let sign: Function = Reflect::get(&subtle, &"sign".into())
        .map_err(|_| Error::Unavailable)?
        .dyn_into()
        .map_err(|_| Error::Unavailable)?;
    let message = Uint8Array::from(message);
    let promise: Promise = sign
        .call3(&subtle, &"RSASSA-PKCS1-v1_5".into(), &key, message.as_ref())
        .map_err(|_| Error::Unavailable)?
        .dyn_into()
        .map_err(|_| Error::Unavailable)?;
    let signature = JsFuture::from(promise)
        .await
        .map_err(|_| Error::Unavailable)?;
    Ok(Uint8Array::new(&signature).to_vec())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn jwt_claims_are_bounded_and_use_the_numeric_app_id() {
        let unsigned = jwt_unsigned(4_673_420, 1_800_000_000);
        let segments = unsigned.split('.').collect::<Vec<_>>();
        let [header, claims] = segments.as_slice() else {
            panic!("expected two JWT segments");
        };
        assert_eq!(
            general_purpose::URL_SAFE_NO_PAD.decode(header).unwrap(),
            br#"{"alg":"RS256","typ":"JWT"}"#
        );
        assert_eq!(
            general_purpose::URL_SAFE_NO_PAD.decode(claims).unwrap(),
            br#"{"iat":1799999940,"exp":1800000540,"iss":"4673420"}"#
        );
    }

    #[test]
    fn exact_repository_path_and_operation_installation_are_required() {
        let repository = RepositoryName::new("dark-factory-build/dark-factory".into()).unwrap();
        assert_eq!(
            repository.installation_url(),
            "https://api.github.com/repos/dark-factory-build/dark-factory/installation"
        );
        assert!(RepositoryName::new("baziyer/../dark-factory".into()).is_err());
        assert!(RepositoryName::new("baziyer/dark factory".into()).is_err());
        let installation: Installation = serde_json::from_str(
            r#"{"id":17,"app_id":4673420,"account":{"id":109233175},"repository_selection":"selected","permissions":{"checks":"read","contents":"write","issues":"write","merge_queues":"write","metadata":"read","pull_requests":"write"},"events":[],"suspended_at":null}"#,
        )
        .unwrap();
        assert!(validate_installation(&installation, 4_673_420, 109_233_175).is_ok());

        let broader: Installation = serde_json::from_str(
            r#"{"id":17,"app_id":4673420,"account":{"id":109233175},"repository_selection":"selected","permissions":{"administration":"write","checks":"read","contents":"write","issues":"write","merge_queues":"write","metadata":"read","pull_requests":"write"},"events":[],"suspended_at":null}"#,
        )
        .unwrap();
        assert!(validate_installation(&broader, 4_673_420, 109_233_175).is_ok());

        let insufficient: Installation = serde_json::from_str(
            r#"{"id":17,"app_id":4673420,"account":{"id":109233175},"repository_selection":"selected","permissions":{"checks":"read","contents":"write","metadata":"read","pull_requests":"read"},"events":[],"suspended_at":null}"#,
        )
        .unwrap();
        assert_eq!(
            validate_installation(&insufficient, 4_673_420, 109_233_175).err(),
            Some(Error::Unavailable)
        );

        // `publish_commit` mints `contents:
        // write`. A read-only installation used to pass here and pass
        // readiness, then fail at token mint where GitHub's refusal reaches
        // the caller as an opaque "authority is unavailable".
        let read_only: Installation = serde_json::from_str(
            r#"{"id":17,"app_id":4673420,"account":{"id":109233175},"repository_selection":"selected","permissions":{"checks":"read","contents":"read","metadata":"read","pull_requests":"write"},"events":[],"suspended_at":null}"#,
        )
        .unwrap();
        assert_eq!(
            validate_installation(&read_only, 4_673_420, 109_233_175).err(),
            Some(Error::Unavailable)
        );

        // The "ok" fixtures above already listed `merge_queues`, so they would
        // pass whether or not the boundary required it -- they read as
        // coverage while asserting nothing. This is the case that proves it:
        // everything else granted, Merge queues absent. Without it an
        // installation that cannot enqueue passes `/readyz`, reports authority
        // ready, and fails every `enqueue_pull_request` at token mint.
        let no_queue: Installation = serde_json::from_str(
            r#"{"id":17,"app_id":4673420,"account":{"id":109233175},"repository_selection":"selected","permissions":{"checks":"read","contents":"write","issues":"write","metadata":"read","pull_requests":"write"},"events":[],"suspended_at":null}"#,
        )
        .unwrap();
        assert_eq!(
            validate_installation(&no_queue, 4_673_420, 109_233_175).err(),
            Some(Error::Unavailable)
        );

        // Read is not enough: enqueueing writes to the queue.
        let queue_read_only: Installation = serde_json::from_str(
            r#"{"id":17,"app_id":4673420,"account":{"id":109233175},"repository_selection":"selected","permissions":{"checks":"read","contents":"write","issues":"write","merge_queues":"read","metadata":"read","pull_requests":"write"},"events":[],"suspended_at":null}"#,
        )
        .unwrap();
        assert_eq!(
            validate_installation(&queue_read_only, 4_673_420, 109_233_175).err(),
            Some(Error::Unavailable)
        );
    }

    #[test]
    fn publication_paths_are_policy_checked() {
        for allowed in [
            "README.md",
            "src/lib.rs",
            "docs/development/WORKFLOW.md",
            "a.b_c-d/e.rs",
            // An ordinary file beside the refused ones stays publishable.
            ".github/ISSUE_TEMPLATE/bug.yml",
            ".github/PULL_REQUEST_TEMPLATE.md",
            ".githubbed/notes.md",
            "docs/codeowners-guidance.md",
            // The protected names are refused segment-exactly, not as
            // substrings: a path that merely carries one stays publishable,
            // including one where the name is a prefix of a longer segment.
            "CODEOWNERS.md",
            "docs/notes-on-CODEOWNERS.md",
            "src/codeowners_test.rs",
            ".github/dependabot.yml.example",
        ] {
            assert!(
                valid_repository_path(allowed).is_ok(),
                "rejected: {allowed}"
            );
        }
        // Workflow files are refused explicitly. GitHub also blocks them without
        // the `workflows` permission this App does not hold, but an agent able
        // to rewrite the CI gating its own work would be escalating authority,
        // so the surface states the boundary rather than inheriting it.
        for refused in [
            ".github/workflows/ci.yml",
            // A tree entry may name a directory as a blob, which replaces the
            // whole subtree. A prefix test needing the trailing slash let
            // `.github/workflows` delete every workflow, and `.github` delete
            // CODEOWNERS and the issue templates, which no permission on this
            // installation protects.
            ".github/workflows",
            ".github",
            // An agent that can rewrite CODEOWNERS can remove itself from
            // required review. Refusing only the tree left that reachable one
            // file at a time.
            "CODEOWNERS",
            ".github/CODEOWNERS",
            "docs/CODEOWNERS",
            ".github/dependabot.yml",
            ".github/dependabot.yaml",
            // Publishing under a protected name turns its blob into a tree,
            // which deletes the file's content -- the same write, reached by
            // making the name a directory. Every by-name refusal is refused
            // as a leading directory too.
            "CODEOWNERS/x",
            ".github/CODEOWNERS/x",
            "docs/CODEOWNERS/x",
            "CODEOWNERS/nested/deep.md",
            ".github/dependabot.yml/x",
            ".github/dependabot.yaml/x",
            ".GitHub/Workflows/ci.yml",
            ".github/workflows/nested/deep.yml",
            "",
            "/etc/passwd",
            "src/",
            "src//lib.rs",
            "../secrets",
            "src/../../etc/passwd",
            ".git/config",
            "src/a\\b.rs",
        ] {
            assert!(
                valid_repository_path(refused).is_err(),
                "accepted: {refused}"
            );
        }
    }

    /// The table that decides whether "the operation did not run" may be
    /// asserted. A class wrongly listed as rejected-before-execution reports
    /// "nothing changed" for something that may have changed.
    #[test]
    fn only_pre_execution_error_classes_are_rejections() {
        let errors = |kinds: &[Option<&str>]| {
            kinds
                .iter()
                .map(|kind| GraphQlError {
                    kind: kind.map(Into::into),
                    path: vec!["enqueuePullRequest".into()],
                })
                .collect::<Vec<_>>()
        };
        let at_path = |kind: &str, path: &[&str]| {
            vec![GraphQlError {
                kind: Some(kind.into()),
                path: path.iter().map(|part| (*part).into()).collect(),
            }]
        };

        assert!(classify_graphql_errors(&[]).is_none());

        for kind in ["NOT_FOUND", "FORBIDDEN", "UNPROCESSABLE", "RATE_LIMITED"] {
            match classify_graphql_errors(&errors(&[Some(kind)])) {
                Some(GraphQlFailure::Rejected(kinds)) => {
                    // The class is recorded, not merely accepted: a refusal
                    // that cannot say which class it was is the untyped
                    // refusal this table exists to prevent (#371).
                    assert_eq!(kinds.to_string(), kind);
                }
                other => panic!("{kind} is rejected before execution, got {other:?}"),
            }
        }

        // An untyped error is GitHub's shape for `Something went wrong while
        // executing your query`, which its docs say may be a timeout -- and a
        // request that timed out server-side may have executed.
        assert!(matches!(
            classify_graphql_errors(&errors(&[None])),
            Some(GraphQlFailure::Unknown)
        ));
        assert!(matches!(
            classify_graphql_errors(&errors(&[Some("SERVICE_UNAVAILABLE")])),
            Some(GraphQlFailure::Unknown)
        ));
        // One unknown class among rejections makes the whole answer unknown:
        // the array describes one response, and any part of it may have run.
        assert!(matches!(
            classify_graphql_errors(&errors(&[Some("FORBIDDEN"), None])),
            Some(GraphQlFailure::Unknown)
        ));

        // A typed error raised while RESOLVING a field is post-execution by
        // construction, whatever its type says. GitHub raises `FORBIDDEN` this
        // way for permission scoping on installation tokens, and because
        // `mergeQueueEntry.id` and `.state` are non-null it would null the
        // entry -- making a landed enqueue look absent and be reported as
        // "nothing changed".
        assert!(matches!(
            classify_graphql_errors(&at_path(
                "FORBIDDEN",
                &["enqueuePullRequest", "mergeQueueEntry", "id"]
            )),
            Some(GraphQlFailure::Unknown)
        ));
        assert!(matches!(
            classify_graphql_errors(&at_path(
                "NOT_FOUND",
                &["enqueuePullRequest", "mergeQueueEntry", "state"]
            )),
            Some(GraphQlFailure::Unknown)
        ));
        // A root-level error keeps its type: this is the shape GitHub
        // documents for a SAML-protected resource, and it is a genuine
        // pre-execution rejection.
        assert!(matches!(
            classify_graphql_errors(&at_path("FORBIDDEN", &["enqueuePullRequest"])),
            Some(GraphQlFailure::Rejected(_))
        ));
        // An error with no path at all is request-level.
        assert!(matches!(
            classify_graphql_errors(&at_path("RATE_LIMITED", &[])),
            Some(GraphQlFailure::Rejected(_))
        ));
    }

    /// GitHub's ordinary refusal shape for a mutation is a populated `data`
    /// whose mutation field is null, *beside* an errors array. Deciding from
    /// the envelope rather than the payload answers "nothing changed" for an
    /// enqueue that may have landed -- the defect this function exists to make
    /// impossible to reintroduce silently.
    #[test]
    fn an_enqueue_outcome_is_decided_from_the_payload_not_the_envelope() {
        let entry = || QueueEntry {
            id: "MQE_kwDOabc".into(),
            state: "QUEUED".into(),
            pull_request: Some(QueueEntryPullRequest {
                number: 329,
                head_ref_oid: "d".repeat(40),
            }),
        };

        let forbidden = RejectionKinds {
            forbidden: true,
            ..RejectionKinds::default()
        };

        // An entry came back. That is the effect, whatever rode alongside it.
        assert!(enqueue_outcome(Some(entry()), None).is_ok());
        assert!(enqueue_outcome(Some(entry()), Some(GraphQlFailure::Rejected(forbidden))).is_ok());
        assert!(enqueue_outcome(Some(entry()), Some(GraphQlFailure::Unknown)).is_ok());

        // No entry, rejected before execution: determinate, the same
        // operation ID stays retryable, and the refusal carries the classes
        // the classification recorded rather than a fresh guess.
        assert!(matches!(
            enqueue_outcome(None, Some(GraphQlFailure::Rejected(forbidden))),
            Err(OperationError::Refused(RefusalReason::Rejected(kinds))) if kinds == forbidden
        ));
        // No entry, and an error class that may follow a server-side timeout
        // on work already under way. Reporting "nothing changed" here is the
        // one answer that can be actively false.
        assert!(matches!(
            enqueue_outcome(None, Some(GraphQlFailure::Unknown)),
            Err(OperationError::Indeterminate)
        ));
        // No entry and no error at all is its own reason: nothing was
        // rejected, GitHub simply answered with no effect.
        assert!(matches!(
            enqueue_outcome(None, None),
            Err(OperationError::Refused(RefusalReason::NoEffect))
        ));
    }

    /// The reason must survive to the caller-visible rendering: each refusal
    /// names itself distinctly, and a multi-class rejection carries every
    /// class rather than whichever arrived first (#371).
    #[test]
    fn a_refusal_renders_its_reason() {
        let mixed = vec![
            GraphQlError {
                kind: Some("NOT_FOUND".into()),
                path: vec!["enqueuePullRequest".into()],
            },
            GraphQlError {
                kind: Some("RATE_LIMITED".into()),
                path: vec![],
            },
        ];
        match classify_graphql_errors(&mixed) {
            Some(GraphQlFailure::Rejected(kinds)) => {
                assert_eq!(kinds.to_string(), "NOT_FOUND+RATE_LIMITED");
            }
            other => panic!("a mixed typed rejection stays rejected, got {other:?}"),
        }

        let rejected = enqueue_outcome(None, classify_graphql_errors(&mixed))
            .err()
            .unwrap();
        assert_eq!(
            rejected.to_string(),
            "github refused the operation and nothing changed: \
             rejected before execution as NOT_FOUND+RATE_LIMITED"
        );
        assert_eq!(
            enqueue_outcome(None, None).err().unwrap().to_string(),
            "github refused the operation and nothing changed: \
             answered with neither an effect nor an error"
        );
        assert_eq!(
            OperationError::Refused(RefusalReason::NoMergeQueue).to_string(),
            "github refused the operation and nothing changed: \
             the queue read found no merge queue on the base branch"
        );
        // The empty set is unreachable from classification but constructible
        // through the derived `Default`; its rendering must say so instead
        // of ending the sentence with nothing.
        assert_eq!(RejectionKinds::default().to_string(), "no recorded class");
    }

    /// The queue entry's own `headCommit` is the queue's synthetic merge
    /// commit, not the pull request head. Binding to the wrong one would make
    /// every reconciliation answer "not queued" for something that is, and the
    /// operation would enqueue a second time.
    #[test]
    fn a_queue_entry_is_bound_to_the_pull_request_head_not_the_queue_commit() {
        let head = "d".repeat(40);
        let request = EnqueuePullRequest {
            operation_id: "4c8a5c44-7f1f-11f0-952e-acde48001122".into(),
            pull_number: 329,
            head_sha: head.clone(),
            base: "main".into(),
        };
        assert!(request.validate().is_ok());

        let entry = |number: i64, oid: &str| QueueEntry {
            id: "MQE_kwDOabc".into(),
            state: "QUEUED".into(),
            pull_request: Some(QueueEntryPullRequest {
                number,
                head_ref_oid: oid.into(),
            }),
        };

        assert!(entry(329, &head).matches(&request));
        // A different pull request's entry is not this operation's effect.
        assert!(!entry(330, &head).matches(&request));
        // Nor is this pull request queued at a head the caller did not state:
        // GitHub ejects an entry when the head moves, so a surviving entry
        // naming another head means the queue holds something else.
        assert!(!entry(329, &"e".repeat(40)).matches(&request));
        // An entry GitHub returned without a pull request tells us nothing.
        assert!(
            !QueueEntry {
                id: "MQE_kwDOabc".into(),
                state: "QUEUED".into(),
                pull_request: None,
            }
            .matches(&request)
        );

        let result = entry(329, &head).into_result(&request).unwrap();
        assert_eq!(result.pull_number, 329);
        assert_eq!(result.head_sha, head);
        assert_eq!(result.entry_id, "MQE_kwDOabc");
        assert_eq!(result.state_when_recorded, "QUEUED");

        // An entry already unmergeable is reported as such rather than as a
        // plain "queued": the caller would otherwise wait for a merge that is
        // never coming.
        let unmergeable = QueueEntry {
            state: "UNMERGEABLE".into(),
            ..entry(329, &head)
        };
        assert_eq!(
            unmergeable
                .into_result(&request)
                .unwrap()
                .state_when_recorded,
            "UNMERGEABLE"
        );

        // A state this build does not know means the schema moved under us;
        // guessing would report a merge state it does not understand.
        assert!(matches!(
            QueueEntry {
                state: "TELEPORTED".into(),
                ..entry(329, &head)
            }
            .into_result(&request),
            Err(OperationError::Indeterminate)
        ));

        // Converting a non-matching entry must never manufacture a result.
        assert!(matches!(
            entry(330, &head).into_result(&request),
            Err(OperationError::Indeterminate)
        ));

        // The base is bound too: a pull request that targets another branch
        // would otherwise be enqueued onto that branch's queue.
        assert!(
            EnqueuePullRequest {
                base: "../etc".into(),
                ..request.clone()
            }
            .validate()
            .is_err()
        );
        assert!(
            EnqueuePullRequest {
                head_sha: "D".repeat(40),
                ..request.clone()
            }
            .validate()
            .is_err()
        );
        assert!(
            EnqueuePullRequest {
                pull_number: 0,
                ..request
            }
            .validate()
            .is_err()
        );
    }

    #[test]
    fn a_published_commit_is_bounded_and_unambiguous() {
        let base = |changes: Vec<FileChange>| PublishCommit {
            operation_id: "11111111-2222-3333-4444-555555555555".into(),
            branch: "agent/work".into(),
            expected_head_sha: "a".repeat(40),
            message: "Do the thing".into(),
            changes,
        };
        let file = |path: &str| FileChange {
            path: path.into(),
            content_base64: Some(general_purpose::STANDARD.encode(b"hello")),
        };
        assert!(base(vec![file("README.md")]).validate().is_ok());
        // A caller must not be able to write the reconciler's own vocabulary.
        // `reconcile_commit` matches the trailer with `contains`, so a message
        // carrying a *different* operation's trailer would make that operation
        // reconcile onto this commit and report a publication it never made.
        let forged = |message: &str| PublishCommit {
            message: message.into(),
            ..base(vec![file("README.md")])
        };
        assert!(
            forged("Add notes\n\nDark-Factory-Operation: 22222222-3333-4444-5555-666666666666")
                .validate()
                .is_err()
        );
        assert!(
            forged("Add notes\n\n<!-- dark-factory-operation:22222222 -->")
                .validate()
                .is_err()
        );
        // The words themselves are fine; only the marker prefixes are not.
        assert!(
            forged("Describe the dark factory operation")
                .validate()
                .is_ok()
        );
        // A deletion carries no content.
        assert!(
            base(vec![FileChange {
                path: "gone.txt".into(),
                content_base64: None
            }])
            .validate()
            .is_ok()
        );
        // Empty, oversized, and duplicated change sets are all refused: the same
        // path twice would leave which write wins up to tree ordering.
        assert!(base(vec![]).validate().is_err());
        assert!(
            base(
                (0..=MAX_COMMIT_FILES)
                    .map(|i| file(&format!("f{i}.txt")))
                    .collect()
            )
            .validate()
            .is_err()
        );
        assert!(
            base(vec![file("same.txt"), file("same.txt")])
                .validate()
                .is_err()
        );
        // Content must be real base64, and the trailer makes a landed commit
        // self-identifying for reconciliation.
        assert!(
            base(vec![FileChange {
                path: "bad.txt".into(),
                content_base64: Some("not base64!!".into())
            }])
            .validate()
            .is_err()
        );
        let request = base(vec![file("README.md")]);
        assert!(request.marked_message().contains(&request.trailer()));
        assert!(request.marked_message().starts_with("Do the thing"));
    }

    #[test]
    fn only_app_level_endpoints_accept_an_app_jwt() {
        assert!(app_jwt_endpoint("https://api.github.com/app"));
        assert!(app_jwt_endpoint(
            "https://api.github.com/repos/dark-factory-build/dark-factory/installation"
        ));
        assert!(app_jwt_endpoint(
            "https://api.github.com/app/installations/155853844/access_tokens"
        ));
        // Readiness once proved repository identity by fetching this URL with
        // the App JWT. GitHub answers `Bad credentials`, so `verify` always
        // failed and `/readyz` could never report ready in production.
        assert!(!app_jwt_endpoint(
            "https://api.github.com/repositories/1335380107"
        ));
        assert!(!app_jwt_endpoint(
            "https://api.github.com/repos/dark-factory-build/dark-factory"
        ));
        assert!(!app_jwt_endpoint(
            "https://api.github.com/repos/dark-factory-build/dark-factory/pulls"
        ));
        // A query or fragment must not be able to supply the suffix.
        assert!(!app_jwt_endpoint(
            "https://api.github.com/repos/dark-factory-build/dark-factory/pulls?x=/installation"
        ));
        assert!(!app_jwt_endpoint(
            "https://api.github.com/repos/dark-factory-build/dark-factory/pulls#/installation"
        ));
        // Dot segments are collapsed by the URL parser after this check runs.
        assert!(!app_jwt_endpoint(
            "https://api.github.com/app/installations/../../repos/dark-factory-build/dark-factory/pulls"
        ));
        // Userinfo must not be mistaken for the host.
        assert!(!app_jwt_endpoint("https://api.github.com@evil.example/app"));
    }

    #[test]
    fn authority_configuration_is_exact_and_all_numeric_ids_are_safe() {
        let key = general_purpose::STANDARD.encode(vec![7_u8; 1_200]);
        assert!(
            AppAuthority::new(
                4_673_420,
                key.clone(),
                PERMISSION_REVISION.into(),
                "dark-factory-build/dark-factory".into(),
                "109233175".into(),
                "1335380107".into(),
            )
            .is_ok()
        );
        assert_eq!(
            AppAuthority::new(
                4_673_420,
                key,
                "broader-v2".into(),
                "dark-factory-build/dark-factory/extra".into(),
                "109233175".into(),
                "1335380107".into(),
            )
            .err(),
            Some(Error::Configuration)
        );
    }

    #[test]
    fn typed_operation_inputs_are_exact_head_bound_and_bounded() {
        let create = CreatePullRequest {
            operation_id: "1c8a5c44-7f1f-11f0-952e-acde48001122".into(),
            head: "feature/maintainer".into(),
            head_sha: "a".repeat(40),
            base: "main".into(),
            base_sha: "b".repeat(40),
            title: "Add maintainer operations".into(),
            body: "Exact-head change.".into(),
            draft: false,
        };
        assert!(create.validate().is_ok());
        assert!(
            create
                .marked_body()
                .ends_with("<!-- dark-factory-operation:1c8a5c44-7f1f-11f0-952e-acde48001122 -->")
        );
        assert!(
            CreatePullRequest {
                head: "../main".into(),
                ..create.clone()
            }
            .validate()
            .is_err()
        );
        assert!(
            CreatePullRequest {
                head_sha: "A".repeat(40),
                ..create.clone()
            }
            .validate()
            .is_err()
        );
        assert!(
            CreatePullRequest {
                base_sha: create.head_sha.clone(),
                ..create
            }
            .validate()
            .is_err()
        );

        let review: SubmitPullRequestReview = serde_json::from_value(serde_json::json!({
            "operation_id": "2c8a5c44-7f1f-11f0-952e-acde48001122",
            "pull_number": 297,
            "head_sha": "c".repeat(40),
            "event": "REQUEST_CHANGES",
            "body": "BLOCK: exact finding"
        }))
        .unwrap();
        assert!(review.validate().is_ok());
        assert!(
            serde_json::from_value::<SubmitPullRequestReview>(serde_json::json!({
                "operation_id": "2c8a5c44-7f1f-11f0-952e-acde48001122",
                "pull_number": 297,
                "head_sha": "c".repeat(40),
                "event": "APPROVE",
                "body": "Cannot self-approve"
            }))
            .is_err()
        );

        let recovered = PullRequestReview {
            id: 1,
            html_url:
                "https://github.com/dark-factory-build/dark-factory/pull/297#pullrequestreview-1"
                    .into(),
            body: Some(review.marked_body()),
            commit_id: review.head_sha.clone(),
            state: "CHANGES_REQUESTED".into(),
        };
        assert!(recovered.matches(&review));
        assert!(
            !PullRequestReview {
                state: "COMMENTED".into(),
                ..recovered
            }
            .matches(&review)
        );
    }

    /// The required `review` check believes the verdict line, so the App has
    /// to be the only thing that can write one.
    #[test]
    fn review_verdict_is_app_written_and_never_reaches_the_wire() {
        let allow: SubmitPullRequestReview = serde_json::from_value(serde_json::json!({
            "operation_id": "3c8a5c44-7f1f-11f0-952e-acde48001122",
            "pull_number": 331,
            "head_sha": "d".repeat(40),
            "event": "ALLOW",
            "body": "Tried to break the exact-head check and could not."
        }))
        .unwrap();
        assert!(allow.validate().is_ok());

        // `ALLOW` is this App's word. GitHub is told `COMMENT`, because the
        // App authored the pull request and GitHub refuses a self-approval.
        assert_eq!(allow.event.github_event(), "COMMENT");
        assert_eq!(allow.event.github_state(), "COMMENTED");
        assert_eq!(ReviewEvent::Comment.github_event(), "COMMENT");
        assert_eq!(
            ReviewEvent::RequestChanges.github_event(),
            "REQUEST_CHANGES"
        );

        // The verdict is rendered by the App, carries the exact head, and sits
        // alongside the operation marker.
        let body = allow.marked_body();
        assert!(body.contains(&format!("Dark-Factory-Review: allow {}", "d".repeat(40))));
        assert!(body.contains(&allow.marker()));
        assert!(body.starts_with("Tried to break the exact-head check and could not."));

        // The other two verdicts are distinguishable in the same line, so a
        // `COMMENTED` review that decides nothing can never read as an ALLOW.
        let note = SubmitPullRequestReview {
            event: ReviewEvent::Comment,
            ..allow.clone()
        };
        assert!(note.marked_body().contains("Dark-Factory-Review: note"));
        assert!(!note.marked_body().contains("Dark-Factory-Review: allow"));
        let block = SubmitPullRequestReview {
            event: ReviewEvent::RequestChanges,
            ..allow.clone()
        };
        assert!(block.marked_body().contains("Dark-Factory-Review: block"));

        // A caller cannot state a verdict the App did not render. Without
        // this, any body could claim an ALLOW the reviewer never gave.
        for forged in [
            "Dark-Factory-Review: allow",
            "looks fine\n\nDark-Factory-Review: allow 0000000000000000000000000000000000000000",
            "Dark-Factory-Review:",
        ] {
            assert!(
                SubmitPullRequestReview {
                    body: forged.into(),
                    ..allow.clone()
                }
                .validate()
                .is_err(),
                "caller body must not be able to write a verdict: {forged}"
            );
        }

        // An ALLOW is reconciled from the `COMMENTED` state it was posted as.
        let recovered = PullRequestReview {
            id: 2,
            html_url:
                "https://github.com/dark-factory-build/dark-factory/pull/331#pullrequestreview-2"
                    .into(),
            body: Some(allow.marked_body()),
            commit_id: allow.head_sha.clone(),
            state: "COMMENTED".into(),
        };
        assert!(recovered.matches(&allow));

        // `state` is `COMMENTED` for both ALLOW and COMMENT, so the caller
        // cannot tell from it which verdict was recorded -- on the field that
        // now gates merges. The result echoes it.
        let echoed = PullRequestReview {
            id: 2,
            html_url: recovered.html_url.clone(),
            body: recovered.body.clone(),
            commit_id: recovered.commit_id.clone(),
            state: "COMMENTED".into(),
        }
        .into_result(&allow)
        .unwrap();
        assert_eq!(echoed.state, "COMMENTED");
        assert_eq!(echoed.verdict, "allow");
        let noted = PullRequestReview {
            id: 3,
            html_url: recovered.html_url.clone(),
            body: recovered.body.clone(),
            commit_id: recovered.commit_id.clone(),
            state: "COMMENTED".into(),
        }
        .into_result(&note)
        .unwrap();
        assert_eq!(noted.state, echoed.state);
        assert_ne!(noted.verdict, echoed.verdict);

        // A result journaled before `verdict` existed must still replay. The
        // journal stores these as JSON and replays them with `from_str`, so a
        // required field would turn a completed operation into a permanent
        // `Unavailable` on every retry after the deploy that added it.
        let replayed: ReviewResult = serde_json::from_str(
            r#"{"review_id":7,"url":"https://github.com/o/r/pull/1#pullrequestreview-7",
                 "head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"COMMENTED"}"#,
        )
        .expect("a pre-verdict journal row must still deserialize");
        assert_eq!(replayed.review_id, 7);
        assert!(replayed.verdict.is_empty());
        // A verdict is bound to one head. The same review against any other
        // commit is not this operation's result.
        assert!(
            !PullRequestReview {
                commit_id: "e".repeat(40),
                ..recovered
            }
            .matches(&allow)
        );
    }
}
