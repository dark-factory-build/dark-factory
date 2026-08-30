#![cfg_attr(not(target_arch = "wasm32"), allow(dead_code))]

#[cfg(feature = "development-sqlite")]
use std::{path::Path, sync::Arc, time::Duration};

#[cfg(feature = "development-sqlite")]
use rusqlite::{Connection, OptionalExtension as _, TransactionBehavior, params};
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
use serde::{Deserialize, Serialize};
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
use sha2::{Digest as _, Sha256};
#[cfg(target_arch = "wasm32")]
use worker::{
    DurableObject, Env, Method, ObjectNamespace, Request, RequestInit, Response, SqlStorage,
    SqlStorageValue, State, durable_object,
};

use crate::maintainer::{Delivery, Disposition};

#[cfg(target_arch = "wasm32")]
pub(crate) const NAMESPACE_BINDING: &str = "DARK_FACTORY_MAINTAINER_DELIVERIES";
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
const DELIVERY_MIGRATION_COMPONENT: &str = "maintainer_webhook";
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
const DELIVERY_MIGRATION_REVISION: &str = "0001";
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
const DELIVERY_MIGRATION_SQL: &str = include_str!("../migrations/0001_maintainer_deliveries.sql");
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
const OPERATION_MIGRATION_COMPONENT: &str = "maintainer_operations";
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
const OPERATION_MIGRATION_REVISION: &str = "0004";
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
const OPERATION_MIGRATION_SQL: &str = include_str!("../migrations/0004_maintainer_operations.sql");
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
const OPERATION_LEGACY_TABLES: [&str; 2] =
    ["maintainer_operations_legacy", "maintainer_operations_0002"];
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
const MIGRATION_TABLE_SQL: &str = "CREATE TABLE IF NOT EXISTS control_plane_migrations (
    component TEXT PRIMARY KEY,
    revision TEXT NOT NULL,
    digest TEXT NOT NULL CHECK (
        length(digest) = 64 AND digest NOT GLOB '*[^0-9a-f]*'
    )
) STRICT;";

#[derive(Debug, thiserror::Error)]
pub(crate) enum Error {
    #[cfg(target_arch = "wasm32")]
    #[error("Durable Object delivery journal is unavailable")]
    Cloudflare(#[from] worker::Error),
    #[cfg(target_arch = "wasm32")]
    #[error("Durable Object delivery journal message is invalid")]
    Json(#[from] serde_json::Error),
    #[cfg(feature = "development-sqlite")]
    #[error("development delivery journal is unavailable")]
    Sqlite(#[from] rusqlite::Error),
    #[cfg(feature = "development-sqlite")]
    #[error("development delivery journal worker failed")]
    Worker(#[from] tokio::task::JoinError),
    #[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
    #[error("delivery journal contains an invalid disposition")]
    InvalidDisposition,
    #[cfg(target_arch = "wasm32")]
    #[error("delivery journal lost a conflicting row")]
    MissingConflict,
    #[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
    #[error("operation journal transition is invalid")]
    InvalidTransition,
    #[error("delivery journal schema differs from the reviewed migration")]
    InvalidSchema,
}

#[derive(Clone)]
pub(crate) enum DeliveryJournal {
    #[cfg(target_arch = "wasm32")]
    Cloudflare(CloudflareJournal),
    #[cfg(feature = "development-sqlite")]
    Sqlite(SqliteJournal),
    #[cfg(all(not(target_arch = "wasm32"), not(feature = "development-sqlite")))]
    #[allow(dead_code)]
    Unavailable,
}

#[derive(Debug, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum Record {
    New,
    Replay(Disposition),
    Conflict,
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct Operation {
    pub(crate) operation_id: String,
    pub(crate) kind: String,
    pub(crate) request_digest: String,
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
#[derive(Debug, Deserialize, Serialize)]
pub(crate) struct OperationObservation {
    pub(crate) kind: String,
    pub(crate) request_digest: String,
    pub(crate) state: String,
    pub(crate) result_json: Option<String>,
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum OperationRecord {
    New,
    Planned,
    Claimed,
    Executing,
    Completed(String),
    Indeterminate,
    Conflict,
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
#[derive(Debug, Deserialize)]
struct StoredOperation {
    kind: String,
    request_digest: String,
    state: String,
    result_json: Option<String>,
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
impl StoredOperation {
    fn record(&self, operation: &Operation) -> Result<OperationRecord, Error> {
        if self.kind != operation.kind || self.request_digest != operation.request_digest {
            return Ok(OperationRecord::Conflict);
        }
        match (self.state.as_str(), self.result_json.as_ref()) {
            ("planned", None) => Ok(OperationRecord::Planned),
            ("executing", None) => Ok(OperationRecord::Executing),
            ("completed", Some(result)) => Ok(OperationRecord::Completed(result.clone())),
            ("indeterminate", None) => Ok(OperationRecord::Indeterminate),
            _ => Err(Error::InvalidTransition),
        }
    }
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
#[derive(Deserialize)]
struct StoredDelivery {
    hook_id: i64,
    target_id: i64,
    target_type: String,
    event: String,
    action: Option<String>,
    body_digest: String,
    secret_revision: String,
    disposition: String,
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
impl StoredDelivery {
    fn matches(&self, delivery: &Delivery) -> bool {
        self.hook_id == delivery.hook_id
            && self.target_id == delivery.target_id
            && self.target_type == delivery.target_type
            && self.event == delivery.event
            && self.action == delivery.action
            && self.body_digest == delivery.body_digest
            && self.secret_revision == delivery.secret_revision
            && self.disposition == delivery.disposition.as_str()
    }

    fn replay(&self, delivery: &Delivery) -> Result<Record, Error> {
        if self.matches(delivery) {
            Ok(Record::Replay(
                Disposition::from_database(&self.disposition).ok_or(Error::InvalidDisposition)?,
            ))
        } else {
            Ok(Record::Conflict)
        }
    }
}

impl DeliveryJournal {
    #[cfg(target_arch = "wasm32")]
    pub(crate) const fn cloudflare(namespace: ObjectNamespace, app_id: i64) -> Self {
        Self::Cloudflare(CloudflareJournal { namespace, app_id })
    }

    #[cfg(feature = "development-sqlite")]
    pub(crate) fn open_development(database: &Path) -> Result<Self, Error> {
        Ok(Self::Sqlite(SqliteJournal::open(database)?))
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn ready(&self) -> Result<(), Error> {
        match self {
            Self::Cloudflare(journal) => journal.ready().await,
        }
    }

    pub(crate) async fn record(&self, _delivery: &Delivery) -> Result<Record, Error> {
        match self {
            #[cfg(target_arch = "wasm32")]
            Self::Cloudflare(journal) => journal.record(_delivery).await,
            #[cfg(feature = "development-sqlite")]
            Self::Sqlite(journal) => {
                let journal = journal.clone();
                let delivery = _delivery.clone();
                tokio::task::spawn_blocking(move || journal.record(&delivery)).await?
            }
            #[cfg(all(not(target_arch = "wasm32"), not(feature = "development-sqlite")))]
            Self::Unavailable => Err(Error::InvalidSchema),
        }
    }

    #[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
    pub(crate) async fn begin_operation(
        &self,
        operation: &Operation,
    ) -> Result<OperationRecord, Error> {
        match self {
            #[cfg(target_arch = "wasm32")]
            Self::Cloudflare(journal) => journal.begin_operation(operation).await,
            #[cfg(feature = "development-sqlite")]
            Self::Sqlite(journal) => {
                let journal = journal.clone();
                let operation = operation.clone();
                tokio::task::spawn_blocking(move || journal.begin_operation(&operation)).await?
            }
            #[cfg(all(not(target_arch = "wasm32"), not(feature = "development-sqlite")))]
            Self::Unavailable => Err(Error::InvalidSchema),
        }
    }

    #[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
    pub(crate) async fn mark_operation(
        &self,
        operation: &Operation,
        transition: OperationTransition,
    ) -> Result<OperationRecord, Error> {
        match self {
            #[cfg(target_arch = "wasm32")]
            Self::Cloudflare(journal) => journal.mark_operation(operation, transition).await,
            #[cfg(feature = "development-sqlite")]
            Self::Sqlite(journal) => {
                let journal = journal.clone();
                let operation = operation.clone();
                tokio::task::spawn_blocking(move || journal.mark_operation(&operation, transition))
                    .await?
            }
            #[cfg(all(not(target_arch = "wasm32"), not(feature = "development-sqlite")))]
            Self::Unavailable => Err(Error::InvalidSchema),
        }
    }

    #[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
    pub(crate) async fn observe_operation(
        &self,
        operation_id: &str,
    ) -> Result<Option<OperationObservation>, Error> {
        match self {
            #[cfg(target_arch = "wasm32")]
            Self::Cloudflare(journal) => journal.observe_operation(operation_id).await,
            #[cfg(feature = "development-sqlite")]
            Self::Sqlite(journal) => {
                let journal = journal.clone();
                let operation_id = operation_id.to_owned();
                tokio::task::spawn_blocking(move || journal.observe_operation(&operation_id))
                    .await?
            }
            #[cfg(all(not(target_arch = "wasm32"), not(feature = "development-sqlite")))]
            Self::Unavailable => Err(Error::InvalidSchema),
        }
    }
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum OperationTransition {
    Executing,
    Completed(String),
    /// GitHub refused determinately and nothing happened, so the claim is
    /// released rather than buried: the same operation ID and request may be
    /// retried once the refused precondition holds. `planned` is the state
    /// `begin_operation` inserts, so this needs no new schema.
    Refused,
    Indeterminate,
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
fn migration_digest(sql: &str) -> String {
    hex::encode(Sha256::digest(sql.as_bytes()))
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
fn normalized_sql(sql: &str) -> String {
    sql.split_whitespace().collect::<Vec<_>>().join(" ")
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
fn expected_stored_sql(sql: &str) -> String {
    normalized_sql(
        sql.trim()
            .trim_end_matches(';')
            .replacen(" IF NOT EXISTS ", " ", 1)
            .as_str(),
    )
}

#[cfg(target_arch = "wasm32")]
#[derive(Clone)]
pub(crate) struct CloudflareJournal {
    namespace: ObjectNamespace,
    app_id: i64,
}

#[cfg(target_arch = "wasm32")]
impl CloudflareJournal {
    async fn ready(&self) -> Result<(), Error> {
        let name = format!("maintainer:{}:ready", self.app_id);
        let stub = self.namespace.get_by_name(&name).inspect_err(|error| {
            worker::console_error!("journal: stub unavailable: {error}");
        })?;
        let response = stub
            .fetch_with_str("https://journal.internal/ready")
            .await
            .inspect_err(|error| {
                worker::console_error!("journal: ready fetch failed: {error}");
            })?;
        if response.status_code() == 200 {
            Ok(())
        } else {
            worker::console_error!("journal: ready returned {}", response.status_code());
            Err(Error::InvalidSchema)
        }
    }

    async fn record(&self, delivery: &Delivery) -> Result<Record, Error> {
        let stub = self
            .namespace
            .get_by_name(&delivery_shard_name(self.app_id, &delivery.delivery_id))?;
        let mut init = RequestInit::new();
        init.with_method(Method::Post)
            .with_body(Some(serde_json::to_string(delivery)?.into()));
        let request = Request::new_with_init("https://journal.internal/record", &init)?;
        let mut response = stub.fetch_with_request(request).await?;
        if response.status_code() != 200 {
            return Err(Error::InvalidSchema);
        }
        Ok(response.json().await?)
    }

    async fn begin_operation(&self, operation: &Operation) -> Result<OperationRecord, Error> {
        self.operation_request("/operation/begin", operation, None)
            .await
    }

    async fn mark_operation(
        &self,
        operation: &Operation,
        transition: OperationTransition,
    ) -> Result<OperationRecord, Error> {
        self.operation_request("/operation/mark", operation, Some(transition))
            .await
    }

    async fn observe_operation(
        &self,
        operation_id: &str,
    ) -> Result<Option<OperationObservation>, Error> {
        #[derive(Serialize)]
        struct Message<'a> {
            operation_id: &'a str,
        }
        let stub = self
            .namespace
            .get_by_name(&operation_shard_name(self.app_id, operation_id))?;
        let mut init = RequestInit::new();
        init.with_method(Method::Post).with_body(Some(
            serde_json::to_string(&Message { operation_id })?.into(),
        ));
        let request = Request::new_with_init("https://journal.internal/operation/observe", &init)?;
        let mut response = stub.fetch_with_request(request).await?;
        if response.status_code() != 200 {
            return Err(Error::InvalidSchema);
        }
        Ok(response.json().await?)
    }

    async fn operation_request(
        &self,
        path: &str,
        operation: &Operation,
        transition: Option<OperationTransition>,
    ) -> Result<OperationRecord, Error> {
        #[derive(Serialize)]
        struct Message<'a> {
            operation: &'a Operation,
            transition: Option<OperationTransition>,
        }
        let stub = self
            .namespace
            .get_by_name(&operation_shard_name(self.app_id, &operation.operation_id))?;
        let mut init = RequestInit::new();
        init.with_method(Method::Post).with_body(Some(
            serde_json::to_string(&Message {
                operation,
                transition,
            })?
            .into(),
        ));
        let request = Request::new_with_init(&format!("https://journal.internal{path}"), &init)?;
        let mut response = stub.fetch_with_request(request).await?;
        if response.status_code() != 200 {
            return Err(Error::InvalidSchema);
        }
        Ok(response.json().await?)
    }
}

#[cfg(target_arch = "wasm32")]
fn delivery_shard_name(app_id: i64, delivery_id: &str) -> String {
    // A sha256-derived byte avoids UUID time-prefix hotspots while keeping
    // every replay identity for one App on the same Durable Object.
    let sha256 = Sha256::digest(delivery_id.as_bytes());
    format!("maintainer:{app_id}:{:02x}", sha256[0])
}

#[cfg(target_arch = "wasm32")]
fn operation_shard_name(app_id: i64, operation_id: &str) -> String {
    let sha256 = Sha256::digest(operation_id.as_bytes());
    format!("maintainer:{app_id}:operation:{:02x}", sha256[0])
}

#[cfg(target_arch = "wasm32")]
#[durable_object]
pub struct MaintainerDeliveryJournal {
    sql: SqlStorage,
}

#[cfg(target_arch = "wasm32")]
impl DurableObject for MaintainerDeliveryJournal {
    fn new(state: State, _env: Env) -> Self {
        let sql = state.storage().sql();
        Self { sql }
    }

    async fn fetch(&self, mut request: Request) -> worker::Result<Response> {
        if initialize_cloudflare_schema(&self.sql).is_err() {
            return Response::error("journal unavailable", 503);
        }
        match (request.method(), request.path().as_str()) {
            (Method::Get, "/ready") => Response::ok("ready"),
            (Method::Post, "/record") => {
                let result = async {
                    let delivery: Delivery = request.json().await?;
                    record_cloudflare(&self.sql, &delivery)
                }
                .await;
                match result {
                    Ok(record) => Response::from_json(&record),
                    Err(_) => Response::error("journal unavailable", 503),
                }
            }
            (Method::Post, "/operation/begin") => {
                #[derive(Deserialize)]
                struct Message {
                    operation: Operation,
                }
                let result = async {
                    let message: Message = request.json().await?;
                    begin_operation_cloudflare(&self.sql, &message.operation)
                }
                .await;
                match result {
                    Ok(record) => Response::from_json(&record),
                    Err(_) => Response::error("journal unavailable", 503),
                }
            }
            (Method::Post, "/operation/mark") => {
                #[derive(Deserialize)]
                struct Message {
                    operation: Operation,
                    transition: OperationTransition,
                }
                let result = async {
                    let message: Message = request.json().await?;
                    mark_operation_cloudflare(&self.sql, &message.operation, message.transition)
                }
                .await;
                match result {
                    Ok(record) => Response::from_json(&record),
                    Err(_) => Response::error("journal unavailable", 503),
                }
            }
            (Method::Post, "/operation/observe") => {
                #[derive(Deserialize)]
                struct Message {
                    operation_id: String,
                }
                let result = async {
                    let message: Message = request.json().await?;
                    observe_operation_cloudflare(&self.sql, &message.operation_id)
                }
                .await;
                match result {
                    Ok(observation) => Response::from_json(&observation),
                    Err(_) => Response::error("journal unavailable", 503),
                }
            }
            _ => Response::error("not found", 404),
        }
    }
}

/// Rebuild an older operations table in place, preserving its rows.
///
/// SQLite cannot alter a CHECK, so the table is renamed, recreated from the
/// current migration, its rows copied, and the old one dropped. A shard
/// already on the current revision, or one with no operations table yet, is
/// left alone.
///
/// This ran once for `0002 -> 0003` to widen an enumerated `kind`, and runs
/// again for `-> 0004`, which replaces that enumeration with a shape check so
/// there is no third time.
#[cfg(target_arch = "wasm32")]
fn table_exists(sql: &SqlStorage, name: &str) -> Result<bool, Error> {
    #[derive(Deserialize)]
    struct Present {
        present: i64,
    }
    let rows = sql
        .exec(
            "SELECT count(*) AS present FROM sqlite_schema WHERE type = 'table' AND name = ?",
            vec![name.into()],
        )?
        .to_array::<Present>()?;
    Ok(matches!(rows.as_slice(), [row] if row.present > 0))
}

/// The statement the rebuild uses to drain one legacy table into the current
/// one. Shared with the test lane because this statement shipped unparseable
/// once already.
///
/// `WHERE true` is required, not stylistic. SQLite cannot parse an UPSERT
/// attached to an `INSERT .. SELECT` without one: the parser cannot tell
/// whether `ON` begins a join constraint or the conflict clause, and answers
/// `near "DO": syntax error`.
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
fn operations_copy_sql(source: &str) -> String {
    format!(
        "INSERT INTO maintainer_operations
             (operation_id, kind, request_digest, state, result_json, created_at, updated_at)
         SELECT operation_id, kind, request_digest, state, result_json, created_at, updated_at
         FROM {source}
         WHERE true
         ON CONFLICT(operation_id) DO NOTHING"
    )
}

/// Find rows that would collide by ID but differ in any persisted field. The
/// copy is allowed to use `DO NOTHING` only after this check: equal duplicates
/// are already present in the rebuilt table, while divergent duplicates must
/// leave both tables intact for manual recovery.
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
fn operations_conflicts_sql(source: &str) -> String {
    format!(
        "SELECT count(*) AS conflicts
         FROM {source} AS legacy
         JOIN maintainer_operations AS current
           ON current.operation_id = legacy.operation_id
         WHERE current.kind IS NOT legacy.kind
            OR current.request_digest IS NOT legacy.request_digest
            OR current.state IS NOT legacy.state
            OR current.result_json IS NOT legacy.result_json
            OR current.created_at IS NOT legacy.created_at
            OR current.updated_at IS NOT legacy.updated_at"
    )
}

/// Whether the stored `CREATE TABLE` text is the current migration's.
///
/// Split out from the storage read so it is host-testable: this predicate
/// decides whether the rebuild runs at all, and getting it wrong in either
/// direction is severe -- a false positive skips a needed rebuild and records
/// the new revision against an old table, which the schema audit then rejects
/// forever.
#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
fn operations_schema_is_current(stored: &str) -> bool {
    normalized_sql(stored) == expected_stored_sql(OPERATION_MIGRATION_SQL)
}

#[cfg(target_arch = "wasm32")]
fn operations_table_is_current(sql: &SqlStorage) -> Result<bool, Error> {
    let rows = sql
        .exec(
            "SELECT sql FROM sqlite_schema
             WHERE type = 'table' AND name = 'maintainer_operations'",
            None,
        )?
        .to_array::<SchemaRow>()?;
    Ok(matches!(rows.as_slice(), [row] if operations_schema_is_current(&row.sql)))
}

#[cfg(target_arch = "wasm32")]
fn operations_have_legacy_tables(sql: &SqlStorage) -> Result<bool, Error> {
    OPERATION_LEGACY_TABLES
        .iter()
        .try_fold(false, |present, name| {
            Ok(present || table_exists(sql, name)?)
        })
}

#[cfg(target_arch = "wasm32")]
fn migrate_operations(sql: &SqlStorage) -> Result<(), Error> {
    // The marker is only a record of completed work. The table and any
    // leftover legacy slots are the recovery state, because a rebuild is not
    // atomic and can fail before its marker update.
    if operations_table_is_current(sql)? && !operations_have_legacy_tables(sql)? {
        return Ok(());
    }
    worker::console_log!(
        "journal: rebuilding maintainer_operations for revision {OPERATION_MIGRATION_REVISION}"
    );

    // `maintainer_operations_0002` is the name an earlier form of this
    // function renamed to. A shard interrupted part-way through that rebuild
    // holds the only copy of its journal under it, so it is still drained
    // here rather than assuming every shard finished.
    // Each step is driven off what the schema actually holds, because the
    // rebuild is not atomic: a failure part-way through returns a 503 and
    // leaves the completed steps in place, and the revision row still names
    // the old revision, so the next request runs this again.
    //
    // The rename is guarded on the live table being the WRONG SHAPE, not on a
    // legacy table being absent. Those are different questions once more than
    // one legacy name is possible: a shard left holding both a current-shape
    // table and an older legacy one would otherwise skip the rename, no-op the
    // `IF NOT EXISTS` create, and record the new revision against a table that
    // never changed -- which the schema audit then rejects forever.
    if !operations_table_is_current(sql)? && table_exists(sql, "maintainer_operations")? {
        let mut free = None;
        for name in OPERATION_LEGACY_TABLES {
            // `?`, not a `matches!` that folds an error into "occupied": a
            // storage failure here is not evidence that a slot is taken.
            if !table_exists(sql, name)? {
                free = Some(name);
                break;
            }
        }
        let Some(slot) = free else {
            // Both names are taken and the live table is still wrong. Renaming
            // over either one would destroy the only copy of those rows.
            worker::console_error!("journal: no free legacy slot for the operations rebuild");
            return Err(Error::InvalidSchema);
        };
        sql.exec(
            &format!("ALTER TABLE maintainer_operations RENAME TO {slot}"),
            None,
        )?;
    }
    sql.exec(OPERATION_MIGRATION_SQL, None)?;
    for name in OPERATION_LEGACY_TABLES {
        if table_exists(sql, name)? {
            #[derive(Deserialize)]
            struct Conflicts {
                conflicts: i64,
            }
            let rows = sql
                .exec(&operations_conflicts_sql(name), None)?
                .to_array::<Conflicts>()?;
            if matches!(rows.as_slice(), [row] if row.conflicts > 0) {
                worker::console_error!(
                    "journal: divergent duplicate in maintainer operations rebuild"
                );
                return Err(Error::InvalidSchema);
            }
            sql.exec(&operations_copy_sql(name), None)?;
            sql.exec(&format!("DROP TABLE {name}"), None)?;
        }
    }
    sql.exec(
        "UPDATE control_plane_migrations SET revision = ?, digest = ? WHERE component = ?",
        vec![
            OPERATION_MIGRATION_REVISION.into(),
            migration_digest(OPERATION_MIGRATION_SQL).into(),
            OPERATION_MIGRATION_COMPONENT.into(),
        ],
    )?;
    Ok(())
}

#[cfg(target_arch = "wasm32")]
fn initialize_cloudflare_schema(sql: &SqlStorage) -> Result<(), Error> {
    sql.exec(MIGRATION_TABLE_SQL, None)?;
    sql.exec(DELIVERY_MIGRATION_SQL, None)?;
    migrate_operations(sql)?;
    sql.exec(OPERATION_MIGRATION_SQL, None)?;
    sql.exec(
        "INSERT INTO control_plane_migrations (component, revision, digest)
         VALUES (?, ?, ?) ON CONFLICT(component) DO NOTHING",
        vec![
            DELIVERY_MIGRATION_COMPONENT.into(),
            DELIVERY_MIGRATION_REVISION.into(),
            migration_digest(DELIVERY_MIGRATION_SQL).into(),
        ],
    )?;
    sql.exec(
        "INSERT INTO control_plane_migrations (component, revision, digest)
         VALUES (?, ?, ?) ON CONFLICT(component) DO UPDATE SET
             revision = excluded.revision, digest = excluded.digest",
        vec![
            OPERATION_MIGRATION_COMPONENT.into(),
            OPERATION_MIGRATION_REVISION.into(),
            migration_digest(OPERATION_MIGRATION_SQL).into(),
        ],
    )?;
    audit_cloudflare_schema(sql)
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct SchemaRow {
    sql: String,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct MigrationRow {
    revision: String,
    digest: String,
}

#[cfg(target_arch = "wasm32")]
fn audit_cloudflare_schema(sql: &SqlStorage) -> Result<(), Error> {
    let delivery_schema = sql
        .exec(
            "SELECT sql FROM sqlite_schema
             WHERE type = 'table' AND name = 'maintainer_deliveries'",
            None,
        )?
        .to_array::<SchemaRow>()?;
    let operation_schema = sql
        .exec(
            "SELECT sql FROM sqlite_schema
             WHERE type = 'table' AND name = 'maintainer_operations'",
            None,
        )?
        .to_array::<SchemaRow>()?;
    let migration_schema = sql
        .exec(
            "SELECT sql FROM sqlite_schema
             WHERE type = 'table' AND name = 'control_plane_migrations'",
            None,
        )?
        .to_array::<SchemaRow>()?;
    let delivery_migration = sql
        .exec(
            "SELECT revision, digest FROM control_plane_migrations WHERE component = ?",
            vec![DELIVERY_MIGRATION_COMPONENT.into()],
        )?
        .to_array::<MigrationRow>()?;
    let operation_migration = sql
        .exec(
            "SELECT revision, digest FROM control_plane_migrations WHERE component = ?",
            vec![OPERATION_MIGRATION_COMPONENT.into()],
        )?
        .to_array::<MigrationRow>()?;
    let exact = delivery_schema.len() == 1
        && normalized_sql(&delivery_schema[0].sql) == expected_stored_sql(DELIVERY_MIGRATION_SQL)
        && operation_schema.len() == 1
        && normalized_sql(&operation_schema[0].sql) == expected_stored_sql(OPERATION_MIGRATION_SQL)
        && migration_schema.len() == 1
        && normalized_sql(&migration_schema[0].sql) == expected_stored_sql(MIGRATION_TABLE_SQL)
        && delivery_migration.len() == 1
        && delivery_migration[0].revision == DELIVERY_MIGRATION_REVISION
        && delivery_migration[0].digest == migration_digest(DELIVERY_MIGRATION_SQL)
        && operation_migration.len() == 1
        && operation_migration[0].revision == OPERATION_MIGRATION_REVISION
        && operation_migration[0].digest == migration_digest(OPERATION_MIGRATION_SQL);
    exact.then_some(()).ok_or(Error::InvalidSchema)
}

#[cfg(target_arch = "wasm32")]
fn record_cloudflare(sql: &SqlStorage, delivery: &Delivery) -> Result<Record, Error> {
    let insert = sql.exec(
        "INSERT INTO maintainer_deliveries (
            delivery_id, hook_id, target_id, target_type, event, action,
            body_digest, disposition, secret_revision
         ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT(delivery_id) DO NOTHING",
        vec![
            delivery.delivery_id.as_str().into(),
            delivery.hook_id.to_string().into(),
            delivery.target_id.to_string().into(),
            delivery.target_type.as_str().into(),
            delivery.event.as_str().into(),
            SqlStorageValue::from(delivery.action.clone()),
            delivery.body_digest.as_str().into(),
            delivery.disposition.as_str().into(),
            delivery.secret_revision.as_str().into(),
        ],
    )?;
    if insert.rows_written() == 1 {
        return Ok(Record::New);
    }
    let stored = sql
        .exec(
            "SELECT hook_id, target_id, target_type, event, action,
                    body_digest, secret_revision, disposition
             FROM maintainer_deliveries WHERE delivery_id = ?",
            vec![delivery.delivery_id.as_str().into()],
        )?
        .to_array::<StoredDelivery>()?;
    match stored.as_slice() {
        [stored] => stored.replay(delivery),
        _ => Err(Error::MissingConflict),
    }
}

#[cfg(target_arch = "wasm32")]
fn begin_operation_cloudflare(
    sql: &SqlStorage,
    operation: &Operation,
) -> Result<OperationRecord, Error> {
    let insert = sql.exec(
        "INSERT INTO maintainer_operations (operation_id, kind, request_digest, state)
         VALUES (?, ?, ?, 'planned') ON CONFLICT(operation_id) DO NOTHING",
        vec![
            operation.operation_id.as_str().into(),
            operation.kind.as_str().into(),
            operation.request_digest.as_str().into(),
        ],
    )?;
    if insert.rows_written() == 1 {
        return Ok(OperationRecord::New);
    }
    stored_operation_cloudflare(sql, operation)
}

#[cfg(target_arch = "wasm32")]
fn stored_operation_cloudflare(
    sql: &SqlStorage,
    operation: &Operation,
) -> Result<OperationRecord, Error> {
    let stored = sql
        .exec(
            "SELECT kind, request_digest, state, result_json
             FROM maintainer_operations WHERE operation_id = ?",
            vec![operation.operation_id.as_str().into()],
        )?
        .to_array::<StoredOperation>()?;
    match stored.as_slice() {
        [stored] => stored.record(operation),
        _ => Err(Error::MissingConflict),
    }
}

#[cfg(target_arch = "wasm32")]
fn observe_operation_cloudflare(
    sql: &SqlStorage,
    operation_id: &str,
) -> Result<Option<OperationObservation>, Error> {
    let stored = sql
        .exec(
            "SELECT kind, request_digest, state, result_json
             FROM maintainer_operations WHERE operation_id = ?",
            vec![operation_id.into()],
        )?
        .to_array::<OperationObservation>()?;
    (stored.len() <= 1)
        .then(|| stored.into_iter().next())
        .ok_or(Error::MissingConflict)
}

#[cfg(target_arch = "wasm32")]
fn mark_operation_cloudflare(
    sql: &SqlStorage,
    operation: &Operation,
    transition: OperationTransition,
) -> Result<OperationRecord, Error> {
    let (state, result, allowed) = transition_parts(&transition)?;
    let update = sql.exec(
        &format!(
            "UPDATE maintainer_operations SET state = ?, result_json = ?, updated_at = unixepoch()
             WHERE operation_id = ? AND kind = ? AND request_digest = ? AND state IN ({allowed})"
        ),
        vec![
            state.into(),
            SqlStorageValue::from(result),
            operation.operation_id.as_str().into(),
            operation.kind.as_str().into(),
            operation.request_digest.as_str().into(),
        ],
    )?;
    if update.rows_written() != 1 {
        return stored_operation_cloudflare(sql, operation);
    }
    if matches!(transition, OperationTransition::Executing) {
        return Ok(OperationRecord::Claimed);
    }
    stored_operation_cloudflare(sql, operation)
}

#[cfg(any(target_arch = "wasm32", feature = "development-sqlite"))]
fn transition_parts(
    transition: &OperationTransition,
) -> Result<(&'static str, Option<String>, &'static str), Error> {
    match transition {
        OperationTransition::Executing => Ok(("executing", None, "'planned'")),
        OperationTransition::Completed(result)
            if (2..=16_384).contains(&result.len())
                && serde_json::from_str::<serde_json::Value>(result).is_ok() =>
        {
            Ok((
                "completed",
                Some(result.clone()),
                "'planned','executing','indeterminate'",
            ))
        }
        // Also from `indeterminate`: a concurrent retry inside the merge
        // round-trip marks the row indeterminate while the original call is
        // still in flight, and that call's refusal would then fail to release
        // the claim — re-wedging the exact operation ID this transition exists
        // to keep retryable. Safe, because the arm issuing this transition is
        // reached only after GitHub answered determinately.
        OperationTransition::Refused => Ok(("planned", None, "'executing','indeterminate'")),
        OperationTransition::Indeterminate => {
            Ok(("indeterminate", None, "'executing','indeterminate'"))
        }
        OperationTransition::Completed(_) => Err(Error::InvalidTransition),
    }
}

#[cfg(feature = "development-sqlite")]
#[derive(Clone)]
pub(crate) struct SqliteJournal {
    database: Arc<Path>,
}

#[cfg(feature = "development-sqlite")]
impl SqliteJournal {
    fn open(database: &Path) -> Result<Self, Error> {
        let journal = Self {
            database: Arc::from(database),
        };
        let connection = journal.connection()?;
        connection.execute_batch(MIGRATION_TABLE_SQL)?;
        connection.execute_batch(DELIVERY_MIGRATION_SQL)?;
        migrate_operations_sqlite(&connection)?;
        connection.execute_batch(OPERATION_MIGRATION_SQL)?;
        connection.execute(
            "INSERT INTO control_plane_migrations (component, revision, digest)
             VALUES (?1, ?2, ?3) ON CONFLICT(component) DO NOTHING",
            params![
                DELIVERY_MIGRATION_COMPONENT,
                DELIVERY_MIGRATION_REVISION,
                migration_digest(DELIVERY_MIGRATION_SQL)
            ],
        )?;
        connection.execute(
            "INSERT INTO control_plane_migrations (component, revision, digest)
             VALUES (?1, ?2, ?3) ON CONFLICT(component) DO UPDATE SET
                 revision = excluded.revision, digest = excluded.digest",
            params![
                OPERATION_MIGRATION_COMPONENT,
                OPERATION_MIGRATION_REVISION,
                migration_digest(OPERATION_MIGRATION_SQL)
            ],
        )?;
        audit_sqlite_schema(&connection)?;
        Ok(journal)
    }

    fn record(&self, delivery: &Delivery) -> Result<Record, Error> {
        let mut connection = self.connection()?;
        audit_sqlite_schema(&connection)?;
        let transaction = connection.transaction_with_behavior(TransactionBehavior::Immediate)?;
        let existing: Option<StoredDelivery> = transaction
            .query_row(
                "SELECT hook_id, target_id, target_type, event, action,
                        body_digest, secret_revision, disposition
                 FROM maintainer_deliveries WHERE delivery_id = ?1",
                [delivery.delivery_id.as_str()],
                |row| {
                    Ok(StoredDelivery {
                        hook_id: row.get(0)?,
                        target_id: row.get(1)?,
                        target_type: row.get(2)?,
                        event: row.get(3)?,
                        action: row.get(4)?,
                        body_digest: row.get(5)?,
                        secret_revision: row.get(6)?,
                        disposition: row.get(7)?,
                    })
                },
            )
            .optional()?;
        if let Some(stored) = existing {
            let result = stored.replay(delivery)?;
            transaction.commit()?;
            return Ok(result);
        }
        transaction.execute(
            "INSERT INTO maintainer_deliveries (
                delivery_id, hook_id, target_id, target_type, event, action,
                body_digest, disposition, secret_revision
             ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)",
            params![
                delivery.delivery_id,
                delivery.hook_id,
                delivery.target_id,
                delivery.target_type,
                delivery.event,
                delivery.action,
                delivery.body_digest,
                delivery.disposition.as_str(),
                delivery.secret_revision,
            ],
        )?;
        transaction.commit()?;
        Ok(Record::New)
    }

    fn begin_operation(&self, operation: &Operation) -> Result<OperationRecord, Error> {
        let mut connection = self.connection()?;
        audit_sqlite_schema(&connection)?;
        let transaction = connection.transaction_with_behavior(TransactionBehavior::Immediate)?;
        let changed = transaction.execute(
            "INSERT INTO maintainer_operations (operation_id, kind, request_digest, state)
             VALUES (?1, ?2, ?3, 'planned') ON CONFLICT(operation_id) DO NOTHING",
            params![
                operation.operation_id,
                operation.kind,
                operation.request_digest
            ],
        )?;
        let record = if changed == 1 {
            OperationRecord::New
        } else {
            stored_operation_sqlite(&transaction, operation)?
        };
        transaction.commit()?;
        Ok(record)
    }

    fn mark_operation(
        &self,
        operation: &Operation,
        transition: OperationTransition,
    ) -> Result<OperationRecord, Error> {
        let mut connection = self.connection()?;
        audit_sqlite_schema(&connection)?;
        let transaction = connection.transaction_with_behavior(TransactionBehavior::Immediate)?;
        let (state, result, allowed) = transition_parts(&transition)?;
        let sql = format!(
            "UPDATE maintainer_operations SET state = ?1, result_json = ?2, updated_at = unixepoch()
             WHERE operation_id = ?3 AND kind = ?4 AND request_digest = ?5 AND state IN ({allowed})"
        );
        let changed = transaction.execute(
            &sql,
            params![
                state,
                result,
                operation.operation_id,
                operation.kind,
                operation.request_digest
            ],
        )?;
        let record = if changed == 1 && matches!(transition, OperationTransition::Executing) {
            OperationRecord::Claimed
        } else {
            stored_operation_sqlite(&transaction, operation)?
        };
        transaction.commit()?;
        Ok(record)
    }

    fn observe_operation(&self, operation_id: &str) -> Result<Option<OperationObservation>, Error> {
        let connection = self.connection()?;
        audit_sqlite_schema(&connection)?;
        observe_operation_sqlite(&connection, operation_id)
    }

    fn connection(&self) -> Result<Connection, rusqlite::Error> {
        let connection = Connection::open(self.database.as_ref())?;
        connection.busy_timeout(Duration::from_secs(5))?;
        Ok(connection)
    }
}

#[cfg(feature = "development-sqlite")]
fn sqlite_table_exists(connection: &Connection, name: &str) -> Result<bool, Error> {
    connection
        .query_row(
            "SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?1",
            [name],
            |row| row.get::<_, i64>(0),
        )
        .map(|count| count > 0)
        .map_err(Error::from)
}

#[cfg(feature = "development-sqlite")]
fn operations_table_is_current_sqlite(connection: &Connection) -> Result<bool, Error> {
    let schema: Option<String> = connection
        .query_row(
            "SELECT sql FROM sqlite_schema
             WHERE type = 'table' AND name = 'maintainer_operations'",
            [],
            |row| row.get(0),
        )
        .optional()?;
    Ok(schema.is_some_and(|sql| operations_schema_is_current(&sql)))
}

#[cfg(feature = "development-sqlite")]
fn operations_have_legacy_tables_sqlite(connection: &Connection) -> Result<bool, Error> {
    OPERATION_LEGACY_TABLES
        .iter()
        .try_fold(false, |present, name| {
            Ok(present || sqlite_table_exists(connection, name)?)
        })
}

#[cfg(feature = "development-sqlite")]
fn sqlite_operations_have_conflicts(connection: &Connection, source: &str) -> Result<bool, Error> {
    connection
        .query_row(&operations_conflicts_sql(source), [], |row| {
            row.get::<_, i64>(0)
        })
        .map(|count| count > 0)
        .map_err(Error::from)
}

#[cfg(feature = "development-sqlite")]
fn migrate_operations_sqlite(connection: &Connection) -> Result<(), Error> {
    if operations_table_is_current_sqlite(connection)?
        && !operations_have_legacy_tables_sqlite(connection)?
    {
        return Ok(());
    }

    if !operations_table_is_current_sqlite(connection)?
        && sqlite_table_exists(connection, "maintainer_operations")?
    {
        let mut free = None;
        for name in OPERATION_LEGACY_TABLES {
            if !sqlite_table_exists(connection, name)? {
                free = Some(name);
                break;
            }
        }
        let Some(slot) = free else {
            return Err(Error::InvalidSchema);
        };
        connection.execute(
            &format!("ALTER TABLE maintainer_operations RENAME TO {slot}"),
            [],
        )?;
    }
    connection.execute_batch(OPERATION_MIGRATION_SQL)?;
    for name in OPERATION_LEGACY_TABLES {
        if sqlite_table_exists(connection, name)? {
            if sqlite_operations_have_conflicts(connection, name)? {
                return Err(Error::InvalidSchema);
            }
            connection.execute_batch(&operations_copy_sql(name))?;
            connection.execute_batch(&format!("DROP TABLE {name}"))?;
        }
    }
    connection.execute(
        "UPDATE control_plane_migrations SET revision = ?1, digest = ?2
         WHERE component = ?3",
        params![
            OPERATION_MIGRATION_REVISION,
            migration_digest(OPERATION_MIGRATION_SQL),
            OPERATION_MIGRATION_COMPONENT,
        ],
    )?;
    Ok(())
}

#[cfg(feature = "development-sqlite")]
fn audit_sqlite_schema(connection: &Connection) -> Result<(), Error> {
    let schema = |name: &str| -> Result<Option<String>, rusqlite::Error> {
        connection
            .query_row(
                "SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?1",
                [name],
                |row| row.get(0),
            )
            .optional()
    };
    let delivery_migration: Option<(String, String)> = connection
        .query_row(
            "SELECT revision, digest FROM control_plane_migrations WHERE component = ?1",
            [DELIVERY_MIGRATION_COMPONENT],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .optional()?;
    let operation_migration: Option<(String, String)> = connection
        .query_row(
            "SELECT revision, digest FROM control_plane_migrations WHERE component = ?1",
            [OPERATION_MIGRATION_COMPONENT],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .optional()?;
    let exact = schema("maintainer_deliveries")?
        .is_some_and(|sql| normalized_sql(&sql) == expected_stored_sql(DELIVERY_MIGRATION_SQL))
        && schema("maintainer_operations")?.is_some_and(|sql| {
            normalized_sql(&sql) == expected_stored_sql(OPERATION_MIGRATION_SQL)
        })
        && schema("control_plane_migrations")?
            .is_some_and(|sql| normalized_sql(&sql) == expected_stored_sql(MIGRATION_TABLE_SQL))
        && delivery_migration.is_some_and(|(revision, digest)| {
            revision == DELIVERY_MIGRATION_REVISION
                && digest == migration_digest(DELIVERY_MIGRATION_SQL)
        })
        && operation_migration.is_some_and(|(revision, digest)| {
            revision == OPERATION_MIGRATION_REVISION
                && digest == migration_digest(OPERATION_MIGRATION_SQL)
        });
    exact.then_some(()).ok_or(Error::InvalidSchema)
}

#[cfg(feature = "development-sqlite")]
fn stored_operation_sqlite(
    connection: &rusqlite::Connection,
    operation: &Operation,
) -> Result<OperationRecord, Error> {
    let stored: StoredOperation = connection.query_row(
        "SELECT kind, request_digest, state, result_json
         FROM maintainer_operations WHERE operation_id = ?1",
        [operation.operation_id.as_str()],
        |row| {
            Ok(StoredOperation {
                kind: row.get(0)?,
                request_digest: row.get(1)?,
                state: row.get(2)?,
                result_json: row.get(3)?,
            })
        },
    )?;
    stored.record(operation)
}

#[cfg(feature = "development-sqlite")]
fn observe_operation_sqlite(
    connection: &rusqlite::Connection,
    operation_id: &str,
) -> Result<Option<OperationObservation>, Error> {
    connection
        .query_row(
            "SELECT kind, request_digest, state, result_json
             FROM maintainer_operations WHERE operation_id = ?1",
            [operation_id],
            |row| {
                Ok(OperationObservation {
                    kind: row.get(0)?,
                    request_digest: row.get(1)?,
                    state: row.get(2)?,
                    result_json: row.get(3)?,
                })
            },
        )
        .optional()
        .map_err(Error::from)
}

#[cfg(all(test, feature = "development-sqlite"))]
mod operation_tests {
    use super::*;

    #[tokio::test]
    async fn a_refused_operation_releases_its_claim_for_the_same_request() {
        let directory = tempfile::tempdir().unwrap();
        let journal =
            DeliveryJournal::open_development(&directory.path().join("journal.db")).unwrap();
        let operation = Operation {
            operation_id: "6d1f0f8e-7f1f-11f0-952e-acde48001122".into(),
            kind: "enqueue_pull_request".into(),
            request_digest: "b".repeat(64),
        };
        assert!(matches!(
            journal.begin_operation(&operation).await.unwrap(),
            OperationRecord::New
        ));
        assert!(matches!(
            journal
                .mark_operation(&operation, OperationTransition::Executing)
                .await
                .unwrap(),
            OperationRecord::Claimed
        ));
        // GitHub refused determinately, so the claim goes back rather than
        // being buried: a merge refused because CI was still running must be
        // retryable under the same operation ID once CI is green.
        assert!(matches!(
            journal
                .mark_operation(&operation, OperationTransition::Refused)
                .await
                .unwrap(),
            OperationRecord::Planned
        ));
        assert!(matches!(
            journal.begin_operation(&operation).await.unwrap(),
            OperationRecord::Planned
        ));
        assert!(matches!(
            journal
                .mark_operation(&operation, OperationTransition::Executing)
                .await
                .unwrap(),
            OperationRecord::Claimed
        ));
        let result = r#"{"pull_number":1,"merged":true}"#.to_owned();
        assert!(
            matches!(journal.mark_operation(&operation, OperationTransition::Completed(result.clone())).await.unwrap(), OperationRecord::Completed(stored) if stored == result)
        );
        // A concurrent retry can mark the row indeterminate while the original
        // call is still waiting on GitHub. That call's refusal must still
        // release the claim, or the operation ID is wedged by exactly the race
        // this transition exists to survive.
        let racing = Operation {
            operation_id: "7e2a1c60-7f1f-11f0-952e-acde48001122".into(),
            kind: "enqueue_pull_request".into(),
            request_digest: "c".repeat(64),
        };
        journal.begin_operation(&racing).await.unwrap();
        journal
            .mark_operation(&racing, OperationTransition::Executing)
            .await
            .unwrap();
        journal
            .mark_operation(&racing, OperationTransition::Indeterminate)
            .await
            .unwrap();
        assert!(matches!(
            journal
                .mark_operation(&racing, OperationTransition::Refused)
                .await
                .unwrap(),
            OperationRecord::Planned
        ));

        // A completed operation is terminal: a late refusal cannot reopen it.
        assert!(
            matches!(journal.mark_operation(&operation, OperationTransition::Refused).await.unwrap(), OperationRecord::Completed(stored) if stored == result)
        );
    }

    #[tokio::test]
    async fn operation_state_is_idempotent_and_conflicts_fail_closed() {
        let directory = tempfile::tempdir().unwrap();
        let journal =
            DeliveryJournal::open_development(&directory.path().join("journal.db")).unwrap();
        let operation = Operation {
            operation_id: "1c8a5c44-7f1f-11f0-952e-acde48001122".into(),
            kind: "create_pull_request".into(),
            request_digest: "a".repeat(64),
        };
        assert!(matches!(
            journal.begin_operation(&operation).await.unwrap(),
            OperationRecord::New
        ));
        assert!(matches!(
            journal.begin_operation(&operation).await.unwrap(),
            OperationRecord::Planned
        ));
        assert!(matches!(
            journal
                .mark_operation(&operation, OperationTransition::Executing)
                .await
                .unwrap(),
            OperationRecord::Claimed
        ));
        assert!(matches!(
            journal.begin_operation(&operation).await.unwrap(),
            OperationRecord::Executing
        ));
        assert!(matches!(
            journal
                .mark_operation(&operation, OperationTransition::Indeterminate)
                .await
                .unwrap(),
            OperationRecord::Indeterminate
        ));
        let result =
            r#"{"number":297,"head_sha":"0123456789012345678901234567890123456789"}"#.to_owned();
        assert!(
            matches!(journal.mark_operation(&operation, OperationTransition::Completed(result.clone())).await.unwrap(), OperationRecord::Completed(stored) if stored == result)
        );
        assert!(
            matches!(journal.begin_operation(&operation).await.unwrap(), OperationRecord::Completed(stored) if stored == result)
        );

        let conflict = Operation {
            kind: "submit_pull_request_review".into(),
            ..operation
        };
        assert!(matches!(
            journal.begin_operation(&conflict).await.unwrap(),
            OperationRecord::Conflict
        ));
    }

    #[tokio::test]
    async fn operation_observation_is_read_only_and_returns_the_stored_result() {
        let directory = tempfile::tempdir().unwrap();
        let journal =
            DeliveryJournal::open_development(&directory.path().join("journal.db")).unwrap();
        let operation = Operation {
            operation_id: "4c8a5c44-7f1f-11f0-952e-acde48001122".into(),
            kind: "publish_commit".into(),
            request_digest: "d".repeat(64),
        };

        assert!(
            journal
                .observe_operation(&operation.operation_id)
                .await
                .unwrap()
                .is_none()
        );
        assert!(matches!(
            journal.begin_operation(&operation).await.unwrap(),
            OperationRecord::New
        ));
        let planned = journal
            .observe_operation(&operation.operation_id)
            .await
            .unwrap()
            .unwrap();
        assert_eq!(planned.kind, operation.kind);
        assert_eq!(planned.request_digest, operation.request_digest);
        assert_eq!(planned.state, "planned");
        assert_eq!(planned.result_json, None);

        assert!(matches!(
            journal
                .mark_operation(&operation, OperationTransition::Executing)
                .await
                .unwrap(),
            OperationRecord::Claimed
        ));
        let result = r#"{"branch":"topic","commit_sha":"0123456789012345678901234567890123456789","parent_sha":"abcdefabcdefabcdefabcdefabcdefabcdefabcd"}"#;
        assert!(matches!(
            journal
                .mark_operation(
                    &operation,
                    OperationTransition::Completed(result.to_owned())
                )
                .await
                .unwrap(),
            OperationRecord::Completed(stored) if stored == result
        ));
        let completed = journal
            .observe_operation(&operation.operation_id)
            .await
            .unwrap()
            .unwrap();
        assert_eq!(completed.kind, operation.kind);
        assert_eq!(completed.request_digest, operation.request_digest);
        assert_eq!(completed.state, "completed");
        assert_eq!(completed.result_json.as_deref(), Some(result));

        // Observation never claims, releases, or changes a completed row.
        assert!(matches!(
            journal.begin_operation(&operation).await.unwrap(),
            OperationRecord::Completed(stored) if stored == result
        ));
    }

    #[tokio::test]
    async fn concurrent_effect_claim_has_exactly_one_winner() {
        let directory = tempfile::tempdir().unwrap();
        let journal =
            DeliveryJournal::open_development(&directory.path().join("journal.db")).unwrap();
        let operation = Operation {
            operation_id: "2c8a5c44-7f1f-11f0-952e-acde48001122".into(),
            kind: "submit_pull_request_review".into(),
            request_digest: "b".repeat(64),
        };
        assert!(matches!(
            journal.begin_operation(&operation).await.unwrap(),
            OperationRecord::New
        ));
        let attempts = (0..8)
            .map(|_| {
                let journal = journal.clone();
                let operation = operation.clone();
                tokio::spawn(async move {
                    journal
                        .mark_operation(&operation, OperationTransition::Executing)
                        .await
                        .unwrap()
                })
            })
            .collect::<Vec<_>>();
        let mut claimed = 0;
        for attempt in attempts {
            if matches!(attempt.await.unwrap(), OperationRecord::Claimed) {
                claimed += 1;
            }
        }
        assert_eq!(claimed, 1);
        assert!(matches!(
            journal.begin_operation(&operation).await.unwrap(),
            OperationRecord::Executing
        ));
    }

    #[tokio::test]
    async fn operation_schema_drift_fails_closed() {
        let directory = tempfile::tempdir().unwrap();
        let database = directory.path().join("journal.db");
        let journal = DeliveryJournal::open_development(&database).unwrap();
        let connection = Connection::open(&database).unwrap();
        connection
            .execute_batch(
                "DROP TABLE maintainer_operations;
                 CREATE TABLE maintainer_operations (operation_id TEXT PRIMARY KEY);",
            )
            .unwrap();
        let operation = Operation {
            operation_id: "3c8a5c44-7f1f-11f0-952e-acde48001122".into(),
            kind: "create_pull_request".into(),
            request_digest: "c".repeat(64),
        };
        assert!(matches!(
            journal.begin_operation(&operation).await,
            Err(Error::InvalidSchema)
        ));
    }
}

/// These tests drive the real rebuild statements against real SQLite. The
/// development journal uses the same schema-driven recovery path as the
/// Durable Object, so missing markers, interrupted rebuilds, and occupied
/// legacy slots are covered without needing a workerd persistence directory.
#[cfg(all(test, feature = "development-sqlite"))]
mod migration_tests {
    use super::*;

    const OPERATION_ID: &str = "2c8a5c44-7f1f-11f0-952e-acde48001122";

    fn database_without_operation_marker(connection: &Connection) {
        connection.execute_batch(MIGRATION_TABLE_SQL).unwrap();
        connection.execute_batch(DELIVERY_MIGRATION_SQL).unwrap();
    }

    fn legacy_0003_table(connection: &Connection, name: &str) {
        connection
            .execute_batch(&format!(
                "CREATE TABLE {name} (
                     operation_id TEXT PRIMARY KEY,
                     kind TEXT NOT NULL,
                     request_digest TEXT NOT NULL,
                     state TEXT NOT NULL,
                     result_json TEXT,
                     created_at INTEGER NOT NULL DEFAULT (unixepoch()),
                     updated_at INTEGER NOT NULL DEFAULT (unixepoch())
                 ) STRICT;
                 INSERT INTO {name}
                     (operation_id, kind, request_digest, state, result_json,
                      created_at, updated_at)
                 VALUES
                    ('{operation_id}',
                      'merge_pull_request_at_head',
                      '{digest}', 'completed', '{{\"merged\":true}}', 1, 1);",
                name = name,
                digest = "a".repeat(64),
                operation_id = OPERATION_ID,
            ))
            .unwrap();
    }

    fn current_operations_table(connection: &Connection) {
        connection.execute_batch(OPERATION_MIGRATION_SQL).unwrap();
        connection
            .execute(
                "INSERT INTO maintainer_operations
                     (operation_id, kind, request_digest, state, result_json,
                      created_at, updated_at)
                 VALUES (?1, ?2, ?3, 'completed', ?4, 1, 1)",
                rusqlite::params![
                    OPERATION_ID,
                    "merge_pull_request_at_head",
                    "a".repeat(64),
                    r#"{"merged":true}"#,
                ],
            )
            .unwrap();
    }

    fn current_schema_with_marker(connection: &Connection, revision: Option<&str>) {
        database_without_operation_marker(connection);
        connection.execute_batch(OPERATION_MIGRATION_SQL).unwrap();
        if let Some(revision) = revision {
            connection
                .execute(
                    "INSERT INTO control_plane_migrations (component, revision, digest)
                     VALUES (?1, ?2, ?3)",
                    rusqlite::params![OPERATION_MIGRATION_COMPONENT, revision, "b".repeat(64),],
                )
                .unwrap();
        }
    }

    #[tokio::test]
    async fn current_schema_repairs_a_missing_marker_after_interrupted_finalization() {
        let directory = tempfile::tempdir().unwrap();
        let database = directory.path().join("journal.db");
        let connection = Connection::open(&database).unwrap();
        current_schema_with_marker(&connection, None);
        drop(connection);

        DeliveryJournal::open_development(&database)
            .expect("a missing finalization marker must be recreated");
        let connection = Connection::open(&database).unwrap();
        let marker: (String, String) = connection
            .query_row(
                "SELECT revision, digest FROM control_plane_migrations
                 WHERE component = ?1",
                [OPERATION_MIGRATION_COMPONENT],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .unwrap();
        assert_eq!(
            marker,
            (
                OPERATION_MIGRATION_REVISION.into(),
                migration_digest(OPERATION_MIGRATION_SQL),
            )
        );
    }

    #[tokio::test]
    async fn current_schema_repairs_a_stale_marker_after_interrupted_finalization() {
        let directory = tempfile::tempdir().unwrap();
        let database = directory.path().join("journal.db");
        let connection = Connection::open(&database).unwrap();
        current_schema_with_marker(&connection, Some("0003"));
        drop(connection);

        DeliveryJournal::open_development(&database)
            .expect("a stale finalization marker must be overwritten");
        let connection = Connection::open(&database).unwrap();
        let marker: (String, String) = connection
            .query_row(
                "SELECT revision, digest FROM control_plane_migrations
                 WHERE component = ?1",
                [OPERATION_MIGRATION_COMPONENT],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .unwrap();
        assert_eq!(
            marker,
            (
                OPERATION_MIGRATION_REVISION.into(),
                migration_digest(OPERATION_MIGRATION_SQL),
            )
        );
    }

    #[tokio::test]
    async fn missing_marker_repairs_an_old_live_table_before_create_if_not_exists() {
        let directory = tempfile::tempdir().unwrap();
        let database = directory.path().join("journal.db");
        let connection = Connection::open(&database).unwrap();
        database_without_operation_marker(&connection);
        legacy_0003_table(&connection, "maintainer_operations");
        drop(connection);

        let journal = DeliveryJournal::open_development(&database)
            .expect("missing operation marker must not skip the live-table rebuild");
        let operation = journal
            .observe_operation(OPERATION_ID)
            .await
            .unwrap()
            .expect("the old row must survive the rebuild");
        assert_eq!(operation.kind, "merge_pull_request_at_head");
        assert_eq!(operation.state, "completed");

        let connection = Connection::open(&database).unwrap();
        let marker: String = connection
            .query_row(
                "SELECT revision FROM control_plane_migrations
                 WHERE component = ?1",
                [OPERATION_MIGRATION_COMPONENT],
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(marker, OPERATION_MIGRATION_REVISION);
    }

    #[tokio::test]
    async fn interrupted_rebuild_drains_a_legacy_slot_without_a_marker() {
        let directory = tempfile::tempdir().unwrap();
        let database = directory.path().join("journal.db");
        let connection = Connection::open(&database).unwrap();
        database_without_operation_marker(&connection);
        legacy_0003_table(&connection, "maintainer_operations_legacy");
        drop(connection);

        let journal = DeliveryJournal::open_development(&database)
            .expect("a legacy-only interrupted rebuild must resume");
        assert!(
            journal
                .observe_operation(OPERATION_ID)
                .await
                .unwrap()
                .is_some()
        );

        let connection = Connection::open(&database).unwrap();
        assert!(!sqlite_table_exists(&connection, "maintainer_operations_legacy").unwrap());
    }

    #[test]
    fn occupied_legacy_slots_fail_closed_without_destroying_rows() {
        let directory = tempfile::tempdir().unwrap();
        let database = directory.path().join("journal.db");
        let connection = Connection::open(&database).unwrap();
        database_without_operation_marker(&connection);
        legacy_0003_table(&connection, "maintainer_operations");
        legacy_0003_table(&connection, "maintainer_operations_legacy");
        legacy_0003_table(&connection, "maintainer_operations_0002");
        drop(connection);

        assert!(matches!(
            DeliveryJournal::open_development(&database),
            Err(Error::InvalidSchema)
        ));

        let connection = Connection::open(&database).unwrap();
        for table in OPERATION_LEGACY_TABLES
            .into_iter()
            .chain(std::iter::once("maintainer_operations"))
        {
            let count: i64 = connection
                .query_row(&format!("SELECT count(*) FROM {table}"), [], |row| {
                    row.get(0)
                })
                .unwrap();
            assert_eq!(count, 1, "migration must preserve {table}");
        }
    }

    #[test]
    fn identical_duplicate_rows_are_safe_to_drain() {
        let directory = tempfile::tempdir().unwrap();
        let database = directory.path().join("journal.db");
        let connection = Connection::open(&database).unwrap();
        database_without_operation_marker(&connection);
        current_operations_table(&connection);
        legacy_0003_table(&connection, "maintainer_operations_legacy");
        drop(connection);

        DeliveryJournal::open_development(&database)
            .expect("identical duplicate rows may be drained");
        let connection = Connection::open(&database).unwrap();
        assert!(!sqlite_table_exists(&connection, "maintainer_operations_legacy").unwrap());
        let count: i64 = connection
            .query_row(
                "SELECT count(*) FROM maintainer_operations
                 WHERE operation_id = ?1",
                [OPERATION_ID],
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(count, 1);
    }

    #[test]
    fn divergent_duplicate_rows_fail_closed_and_keep_both_tables() {
        let directory = tempfile::tempdir().unwrap();
        let database = directory.path().join("journal.db");
        let connection = Connection::open(&database).unwrap();
        database_without_operation_marker(&connection);
        current_operations_table(&connection);
        legacy_0003_table(&connection, "maintainer_operations_legacy");
        connection
            .execute(
                "UPDATE maintainer_operations_legacy SET kind = 'different_kind'",
                [],
            )
            .unwrap();
        drop(connection);

        assert!(matches!(
            DeliveryJournal::open_development(&database),
            Err(Error::InvalidSchema)
        ));

        let connection = Connection::open(&database).unwrap();
        for table in ["maintainer_operations", "maintainer_operations_legacy"] {
            let kind: String = connection
                .query_row(
                    &format!("SELECT kind FROM {table} WHERE operation_id = ?1"),
                    [OPERATION_ID],
                    |row| row.get(0),
                )
                .unwrap();
            assert_eq!(
                kind,
                if table == "maintainer_operations" {
                    "merge_pull_request_at_head"
                } else {
                    "different_kind"
                }
            );
        }
    }

    /// The copy statement must parse. It did not: an UPSERT attached to an
    /// `INSERT .. SELECT` needs a `WHERE` clause, and without one SQLite
    /// answers `near "DO": syntax error`. Every already-used shard would have
    /// renamed its table, failed here, and returned 503 for every route
    /// forever with its journal stranded under the renamed table.
    #[test]
    fn the_rebuild_copy_statement_parses_and_preserves_rows() {
        let connection = Connection::open_in_memory().unwrap();
        legacy_0003_table(&connection, "maintainer_operations_legacy");
        connection.execute_batch(OPERATION_MIGRATION_SQL).unwrap();

        connection
            .execute_batch(&operations_copy_sql("maintainer_operations_legacy"))
            .expect("the rebuild copy statement must be valid SQLite");

        let (count, kind): (i64, String) = connection
            .query_row(
                "SELECT count(*), max(kind) FROM maintainer_operations",
                [],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .unwrap();
        assert_eq!(count, 1, "the legacy row must survive the rebuild");
        // A kind the current build no longer writes still has to round-trip:
        // the new CHECK constrains shape, not membership, so history is kept.
        assert_eq!(kind, "merge_pull_request_at_head");
    }

    /// Re-running the copy after a partial rebuild must not fail or duplicate.
    #[test]
    fn the_rebuild_copy_is_idempotent() {
        let connection = Connection::open_in_memory().unwrap();
        legacy_0003_table(&connection, "maintainer_operations_legacy");
        connection.execute_batch(OPERATION_MIGRATION_SQL).unwrap();

        for _ in 0..3 {
            connection
                .execute_batch(&operations_copy_sql("maintainer_operations_legacy"))
                .unwrap();
        }
        let count: i64 = connection
            .query_row("SELECT count(*) FROM maintainer_operations", [], |row| {
                row.get(0)
            })
            .unwrap();
        assert_eq!(count, 1);
    }

    /// The predicate that decides whether the rebuild runs at all. A false
    /// positive skips a needed rebuild and stamps the new revision onto an old
    /// table, which the schema audit then rejects on every request forever.
    #[test]
    fn the_current_schema_predicate_recognises_only_the_current_table() {
        let connection = Connection::open_in_memory().unwrap();
        connection.execute_batch(OPERATION_MIGRATION_SQL).unwrap();
        let stored: String = connection
            .query_row(
                "SELECT sql FROM sqlite_schema
                 WHERE type = 'table' AND name = 'maintainer_operations'",
                [],
                |row| row.get(0),
            )
            .unwrap();
        assert!(
            operations_schema_is_current(&stored),
            "a table created from the current migration must be recognised"
        );

        // An older shape -- the enumerated `kind` CHECK 0004 replaced -- must
        // not be, or the shard that most needs the rebuild never gets it.
        assert!(!operations_schema_is_current(
            "CREATE TABLE maintainer_operations (
                 operation_id TEXT PRIMARY KEY,
                 kind TEXT NOT NULL CHECK (kind IN ('create_pull_request')),
                 request_digest TEXT NOT NULL,
                 state TEXT NOT NULL,
                 result_json TEXT,
                 created_at INTEGER NOT NULL,
                 updated_at INTEGER NOT NULL
             ) STRICT"
        ));
        assert!(!operations_schema_is_current(""));
    }

    /// Every `kind` the code writes must satisfy the shape CHECK that replaced
    /// the enumeration, and a malformed one must still be refused.
    #[test]
    fn the_kind_shape_check_accepts_every_kind_the_code_writes() {
        let connection = Connection::open_in_memory().unwrap();
        connection.execute_batch(OPERATION_MIGRATION_SQL).unwrap();
        let insert = |id: &str, kind: &str| {
            connection.execute(
                "INSERT INTO maintainer_operations
                     (operation_id, kind, request_digest, state)
                 VALUES (?, ?, ?, 'planned')",
                rusqlite::params![id, kind, "b".repeat(64)],
            )
        };
        for (index, kind) in [
            "create_pull_request",
            "submit_pull_request_review",
            "publish_commit",
            "enqueue_pull_request",
        ]
        .into_iter()
        .enumerate()
        {
            let id = format!("2c8a5c44-7f1f-11f0-952e-acde4800112{index}");
            insert(&id, kind).unwrap_or_else(|error| panic!("{kind} rejected: {error}"));
        }
        assert!(insert("3c8a5c44-7f1f-11f0-952e-acde48001122", "Publish Commit").is_err());
        assert!(insert("4c8a5c44-7f1f-11f0-952e-acde48001122", "").is_err());
    }
}
