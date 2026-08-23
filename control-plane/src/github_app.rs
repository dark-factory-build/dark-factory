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
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, thiserror::Error)]
pub(crate) enum OperationError {
    #[error("maintainer operation input is invalid")]
    InvalidInput,
    #[error("operation ID is already bound to a different request")]
    Conflict,
    #[error("operation outcome requires reconciliation")]
    Indeterminate,
    #[error("maintainer operation authority is unavailable")]
    Unavailable,
}

impl From<Error> for OperationError {
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

#[derive(Clone, Copy, Debug, Deserialize, Serialize)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub(crate) enum ReviewEvent {
    Comment,
    RequestChanges,
}

impl ReviewEvent {
    const fn github_state(self) -> &'static str {
        match self {
            Self::Comment => "COMMENTED",
            Self::RequestChanges => "CHANGES_REQUESTED",
        }
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct ObservePullRequestChecks {
    pub(crate) pull_number: i64,
    pub(crate) head_sha: String,
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
        format!("<!-- dark-factory-operation:{} -->", self.operation_id)
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
        valid_text(&self.body, 1, 16_000, true)
    }

    #[cfg(target_arch = "wasm32")]
    fn operation(&self, kind: &str) -> Result<Operation, OperationError> {
        operation(kind, &self.operation_id, self)
    }

    fn marker(&self) -> String {
        format!("<!-- dark-factory-operation:{} -->", self.operation_id)
    }

    fn marked_body(&self) -> String {
        format!("{}\n\n{}", self.body, self.marker())
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
        // An App JWT authenticates only App-level endpoints; every other REST
        // resource needs an installation token. Minting a least-privilege token
        // proves the key, the App, the installation, and the exact repository
        // binding in one authenticated response, because `installation_token`
        // already requires the granted repositories to be exactly this
        // repository's id, full name, and owner id.
        self.installation_token(BTreeMap::from([("metadata", "read")]))
            .await
            .map(|_| ())
            .map_err(|_| Error::Unavailable)
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
        let response: InstallationToken = github_json_request(
            worker::Method::Post,
            &format!(
                "https://api.github.com/app/installations/{}/access_tokens",
                installation.id
            ),
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
        let matches = reviews
            .into_iter()
            .filter(|review| review.matches(request))
            .map(ReviewResult::try_from)
            .collect::<Result<Vec<_>, _>>()?;
        match matches.as_slice() {
            [] => Ok(None),
            [result] => Ok(Some(ReviewResult {
                review_id: result.review_id,
                url: result.url.clone(),
                head_sha: result.head_sha.clone(),
                state: result.state.clone(),
            })),
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
            event: ReviewEvent,
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
                event: request.event,
            }),
        )
        .await?;
        let result = ReviewResult::try_from(review)?;
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

#[cfg(target_arch = "wasm32")]
impl TryFrom<PullRequestReview> for ReviewResult {
    type Error = OperationError;

    fn try_from(review: PullRequestReview) -> Result<Self, Self::Error> {
        valid_exact_integer(review.id)?;
        valid_github_url(&review.html_url)?;
        valid_sha(&review.commit_id)?;
        if !matches!(review.state.as_str(), "COMMENTED" | "CHANGES_REQUESTED") {
            return Err(OperationError::Unavailable);
        }
        Ok(Self {
            review_id: review.id,
            url: review.html_url,
            head_sha: review.commit_id,
            state: review.state,
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

#[cfg(target_arch = "wasm32")]
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
    if installation.id <= 0
        || installation.app_id != app_id
        || installation.account.id != owner_id
        || installation.repository_selection != "selected"
        || !permission_at_least(&installation.permissions, "checks", "read")
        || !permission_at_least(&installation.permissions, "contents", "read")
        || !permission_at_least(&installation.permissions, "metadata", "read")
        || !permission_at_least(&installation.permissions, "pull_requests", "write")
        || !installation.events.is_empty()
        || installation.suspended_at.is_some()
    {
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
    url == "https://api.github.com/app"
        || url.starts_with("https://api.github.com/app/installations/")
        || (url.starts_with("https://api.github.com/repos/") && url.ends_with("/installation"))
}

#[cfg(target_arch = "wasm32")]
async fn github_json_as_app<T: serde::de::DeserializeOwned>(
    url: &str,
    jwt: &str,
) -> Result<T, Error> {
    app_jwt_endpoint(url)
        .then_some(())
        .ok_or(Error::Unavailable)?;
    github_json(url, jwt).await
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
    headers
        .set("accept", "application/vnd.github+json")
        .map_err(|_| Error::Unavailable)?;
    headers
        .set("authorization", authorization.as_str())
        .map_err(|_| Error::Unavailable)?;
    headers
        .set("user-agent", "dark-factory-control-plane/0.1")
        .map_err(|_| Error::Unavailable)?;
    headers
        .set("x-github-api-version", GITHUB_API_VERSION)
        .map_err(|_| Error::Unavailable)?;
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
        .with_redirect(RequestRedirect::Error)
        .with_headers(headers)
        .with_body(body.map(Into::into));
    let request = Request::new_with_init(url, &init).map_err(|_| Error::Unavailable)?;
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
        return Err(Error::Unavailable);
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
    serde_json::from_slice(&bytes).map_err(|_| Error::Unavailable)
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
        let repository = RepositoryName::new("baziyer/dark-factory".into()).unwrap();
        assert_eq!(
            repository.installation_url(),
            "https://api.github.com/repos/baziyer/dark-factory/installation"
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
    }

    #[test]
    fn only_app_level_endpoints_accept_an_app_jwt() {
        assert!(app_jwt_endpoint("https://api.github.com/app"));
        assert!(app_jwt_endpoint(
            "https://api.github.com/repos/baziyer/dark-factory/installation"
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
            "https://api.github.com/repos/baziyer/dark-factory"
        ));
        assert!(!app_jwt_endpoint(
            "https://api.github.com/repos/baziyer/dark-factory/pulls"
        ));
    }

    #[test]
    fn authority_configuration_is_exact_and_all_numeric_ids_are_safe() {
        let key = general_purpose::STANDARD.encode(vec![7_u8; 1_200]);
        assert!(
            AppAuthority::new(
                4_673_420,
                key.clone(),
                PERMISSION_REVISION.into(),
                "baziyer/dark-factory".into(),
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
                "baziyer/dark-factory/extra".into(),
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
            html_url: "https://github.com/baziyer/dark-factory/pull/297#pullrequestreview-1".into(),
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
}
