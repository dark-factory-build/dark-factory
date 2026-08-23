use axum::{
    body::Bytes,
    extract::State,
    http::{HeaderMap, StatusCode, header::CONTENT_TYPE},
    response::{IntoResponse as _, Response},
};
use serde_json::{Map, Value, json};

use crate::{
    BrokerState,
    access::AccessAuthority,
    github_app::{
        AppAuthority, CreatePullRequest, MergePullRequest, ObservePullRequestChecks,
        OperationError, PublishCommit, SubmitPullRequestReview,
    },
    journal::DeliveryJournal,
};

pub(crate) const PATH: &str = "/mcp";
const PROTOCOL_VERSION: &str = "2025-06-18";

#[derive(Clone)]
pub(crate) struct McpState {
    access: AccessAuthority,
    app: AppAuthority,
    journal: DeliveryJournal,
}

impl McpState {
    pub(crate) const fn new(
        access: AccessAuthority,
        app: AppAuthority,
        journal: DeliveryJournal,
    ) -> Self {
        Self {
            access,
            app,
            journal,
        }
    }

    /// Cloudflare Access is this surface's own live dependency, and the App
    /// authority has already been proved by the maintainer readiness path, so
    /// this adds a signal instead of repeating one.
    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn ready(&self) -> Result<(), ()> {
        self.access.ready().await.map_err(|_| ())
    }
}

#[worker::send]
pub(crate) async fn receive(
    State(state): State<BrokerState>,
    headers: HeaderMap,
    body: Bytes,
) -> Response {
    let Some(mcp) = state.mcp.as_ref() else {
        return error_response(StatusCode::SERVICE_UNAVAILABLE, "inactive");
    };
    if mcp.access.authorize(&headers).await.is_err() {
        return error_response(StatusCode::UNAUTHORIZED, "unauthorized");
    }
    match single_header(&headers, CONTENT_TYPE.as_str()) {
        Some(value) if value == "application/json" || value.starts_with("application/json;") => {}
        _ => return error_response(StatusCode::UNSUPPORTED_MEDIA_TYPE, "invalid_content_type"),
    }
    let request: Value = match serde_json::from_slice(&body) {
        Ok(request) => request,
        Err(_) => return json_rpc_error(Value::Null, -32700, "Parse error"),
    };
    dispatch(request, mcp).await
}

async fn dispatch(request: Value, mcp: &McpState) -> Response {
    let Some(request) = request.as_object() else {
        return json_rpc_error(Value::Null, -32600, "Invalid Request");
    };
    if request.get("jsonrpc").and_then(Value::as_str) != Some("2.0") {
        return json_rpc_error(Value::Null, -32600, "Invalid Request");
    }
    let Some(method) = request.get("method").and_then(Value::as_str) else {
        return json_rpc_error(Value::Null, -32600, "Invalid Request");
    };
    let Some(id) = request_id(request.get("id")) else {
        return StatusCode::ACCEPTED.into_response();
    };
    match method {
        "initialize" => json_rpc_result(
            id,
            json!({
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {"tools": {"listChanged": false}},
                "serverInfo": {"name": "dark-factory-maintainer", "version": "0.1.0"},
                "instructions": "Use only the repository-bound tools. Read status before a write. Every write is exact-head bound and may fail closed."
            }),
        ),
        "tools/list" => json_rpc_result(id, tools()),
        "tools/call" => call_tool(id, request, mcp).await,
        _ => json_rpc_error(id, -32601, "Method not found"),
    }
}

fn tools() -> Value {
    json!({"tools": [{
        "name": "maintainer_status",
        "title": "Verify maintainer authority",
        "description": "Verify the exact Dark Factory Maintainer GitHub App installation and permission revision before repository operations.",
        "inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
        "outputSchema": {
            "type": "object",
            "properties": {
                "repository": {"type": "string"},
                "repository_id": {"type": "integer"},
                "permission_revision": {"type": "string"}
            },
            "required": ["repository", "repository_id", "permission_revision"],
            "additionalProperties": false
        },
        "annotations": {"readOnlyHint": true, "destructiveHint": false, "openWorldHint": true}
    }, {
        "name": "create_pull_request",
        "title": "Create an exact-head pull request",
        "description": "Create one repository-bound pull request after verifying the exact head and base commit IDs. Replays require the same operation UUID and request.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "operation_id": {"type": "string", "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"},
                "head": {"type": "string", "minLength": 1, "maxLength": 240},
                "head_sha": {"type": "string", "pattern": "^[0-9a-f]{40}$"},
                "base": {"type": "string", "minLength": 1, "maxLength": 240},
                "base_sha": {"type": "string", "pattern": "^[0-9a-f]{40}$"},
                "title": {"type": "string", "minLength": 1, "maxLength": 256},
                "body": {"type": "string", "maxLength": 30000},
                "draft": {"type": "boolean"}
            },
            "required": ["operation_id", "head", "head_sha", "base", "base_sha", "title", "body", "draft"],
            "additionalProperties": false
        },
        "outputSchema": {
            "type": "object",
            "properties": {
                "number": {"type": "integer"},
                "url": {"type": "string"},
                "head_sha": {"type": "string"},
                "base_sha": {"type": "string"}
            },
            "required": ["number", "url", "head_sha", "base_sha"],
            "additionalProperties": false
        },
        "annotations": {"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}
    }, {
        "name": "submit_pull_request_review",
        "title": "Submit an exact-head pull request review",
        "description": "Submit a COMMENT or REQUEST_CHANGES review bound to one pull request head commit. Replays require the same operation UUID and request.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "operation_id": {"type": "string", "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"},
                "pull_number": {"type": "integer", "minimum": 1},
                "head_sha": {"type": "string", "pattern": "^[0-9a-f]{40}$"},
                "event": {"type": "string", "enum": ["COMMENT", "REQUEST_CHANGES"]},
                "body": {"type": "string", "minLength": 1, "maxLength": 16000}
            },
            "required": ["operation_id", "pull_number", "head_sha", "event", "body"],
            "additionalProperties": false
        },
        "outputSchema": {
            "type": "object",
            "properties": {
                "review_id": {"type": "integer"},
                "url": {"type": "string"},
                "head_sha": {"type": "string"},
                "state": {"type": "string"}
            },
            "required": ["review_id", "url", "head_sha", "state"],
            "additionalProperties": false
        },
        "annotations": {"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}
    }, {
        "name": "observe_pull_request_checks",
        "title": "Observe exact-head pull request checks",
        "description": "Return the complete bounded set of GitHub check runs for one exact pull request head commit.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "pull_number": {"type": "integer", "minimum": 1},
                "head_sha": {"type": "string", "pattern": "^[0-9a-f]{40}$"}
            },
            "required": ["pull_number", "head_sha"],
            "additionalProperties": false
        },
        "outputSchema": {
            "type": "object",
            "properties": {
                "pull_number": {"type": "integer"},
                "head_sha": {"type": "string"},
                "checks": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "name": {"type": "string"},
                            "status": {"type": "string"},
                            "conclusion": {"type": ["string", "null"]},
                            "url": {"type": "string"}
                        },
                        "required": ["name", "status", "conclusion", "url"],
                        "additionalProperties": false
                    }
                }
            },
            "required": ["pull_number", "head_sha", "checks"],
            "additionalProperties": false
        },
        "annotations": {"readOnlyHint": true, "destructiveHint": false, "openWorldHint": true}
    }, {
        "name": "publish_commit",
        "title": "Publish an exact-head commit",
        "description": "Publish one commit to a repository branch, but only while that branch still points at the stated commit. A file with no content is deleted. Workflow files cannot be written. Replays require the same operation UUID and request.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "operation_id": {"type": "string", "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"},
                "branch": {"type": "string", "minLength": 1, "maxLength": 240},
                "expected_head_sha": {"type": "string", "pattern": "^[0-9a-f]{40}$"},
                "message": {"type": "string", "minLength": 1, "maxLength": 4096},
                "changes": {
                    "type": "array",
                    "minItems": 1,
                    "maxItems": 50,
                    "items": {
                        "type": "object",
                        "properties": {
                            "path": {"type": "string", "minLength": 1, "maxLength": 240},
                            "content_base64": {"type": "string", "maxLength": 1000000}
                        },
                        "required": ["path"],
                        "additionalProperties": false
                    }
                }
            },
            "required": ["operation_id", "branch", "expected_head_sha", "message", "changes"],
            "additionalProperties": false
        },
        "outputSchema": {
            "type": "object",
            "properties": {
                "branch": {"type": "string"},
                "commit_sha": {"type": "string"},
                "parent_sha": {"type": "string"}
            },
            "required": ["branch", "commit_sha", "parent_sha"],
            "additionalProperties": false
        },
        "annotations": {"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}
    }, {
        "name": "merge_pull_request_at_head",
        "title": "Merge a pull request at an exact head",
        "description": "Merge one pull request only while its head is still the stated commit. GitHub refuses the merge if the head moved or a required check has not passed. Replays require the same operation UUID and request.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "operation_id": {"type": "string", "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"},
                "pull_number": {"type": "integer", "minimum": 1},
                "head_sha": {"type": "string", "pattern": "^[0-9a-f]{40}$"},
                "merge_method": {"type": "string", "enum": ["merge", "squash", "rebase"]}
            },
            "required": ["operation_id", "pull_number", "head_sha", "merge_method"],
            "additionalProperties": false
        },
        "outputSchema": {
            "type": "object",
            "properties": {
                "pull_number": {"type": "integer"},
                "merged": {"type": "boolean"},
                "merge_commit_sha": {"type": "string"},
                "head_sha": {"type": "string"}
            },
            "required": ["pull_number", "merged", "merge_commit_sha", "head_sha"],
            "additionalProperties": false
        },
        "annotations": {"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": true}
    }]})
}

async fn call_tool(id: Value, request: &Map<String, Value>, mcp: &McpState) -> Response {
    let params = request.get("params").and_then(Value::as_object);
    let name = params
        .and_then(|params| params.get("name"))
        .and_then(Value::as_str);
    let arguments = params
        .and_then(|params| params.get("arguments"))
        .cloned()
        .unwrap_or_else(|| json!({}));
    match name {
        Some("maintainer_status")
            if arguments.as_object().is_some_and(serde_json::Map::is_empty) =>
        {
            if mcp.app.verify().await.is_err() {
                return tool_error(id, "unavailable", "Maintainer authority is unavailable.");
            }
            tool_result(
                id,
                json!({
                    "repository": mcp.app.repository(),
                    "repository_id": mcp.app.repository_id(),
                    "permission_revision": mcp.app.permission_revision()
                }),
                "Dark Factory Maintainer authority is ready.",
            )
        }
        Some("create_pull_request") => {
            let Ok(arguments) = serde_json::from_value::<CreatePullRequest>(arguments) else {
                return json_rpc_error(id, -32602, "Invalid params");
            };
            match mcp.app.create_pull_request(&mcp.journal, arguments).await {
                Ok(result) => {
                    serialized_tool_result(id, &result, "Pull request is durably recorded.")
                }
                Err(error) => operation_error(id, error),
            }
        }
        Some("submit_pull_request_review") => {
            let Ok(arguments) = serde_json::from_value::<SubmitPullRequestReview>(arguments) else {
                return json_rpc_error(id, -32602, "Invalid params");
            };
            match mcp
                .app
                .submit_pull_request_review(&mcp.journal, arguments)
                .await
            {
                Ok(result) => {
                    serialized_tool_result(id, &result, "Pull request review is durably recorded.")
                }
                Err(error) => operation_error(id, error),
            }
        }
        Some("publish_commit") => {
            let Ok(arguments) = serde_json::from_value::<PublishCommit>(arguments) else {
                return json_rpc_error(id, -32602, "Invalid params");
            };
            match mcp.app.publish_commit(&mcp.journal, arguments).await {
                Ok(result) => serialized_tool_result(id, &result, "Commit is durably published."),
                Err(error) => operation_error(id, error),
            }
        }
        Some("merge_pull_request_at_head") => {
            let Ok(arguments) = serde_json::from_value::<MergePullRequest>(arguments) else {
                return json_rpc_error(id, -32602, "Invalid params");
            };
            match mcp
                .app
                .merge_pull_request_at_head(&mcp.journal, arguments)
                .await
            {
                Ok(result) => {
                    serialized_tool_result(id, &result, "Pull request is durably merged.")
                }
                Err(error) => operation_error(id, error),
            }
        }
        Some("observe_pull_request_checks") => {
            let Ok(arguments) = serde_json::from_value::<ObservePullRequestChecks>(arguments)
            else {
                return json_rpc_error(id, -32602, "Invalid params");
            };
            match mcp.app.observe_pull_request_checks(arguments).await {
                Ok(result) => {
                    serialized_tool_result(id, &result, "Exact-head check runs were observed.")
                }
                Err(error) => operation_error(id, error),
            }
        }
        _ => json_rpc_error(id, -32602, "Invalid params"),
    }
}

fn tool_result(id: Value, structured: Value, message: &'static str) -> Response {
    json_rpc_result(
        id,
        json!({
            "structuredContent": structured,
            "content": [{"type": "text", "text": message}],
            "isError": false
        }),
    )
}

fn serialized_tool_result<T: serde::Serialize>(
    id: Value,
    result: &T,
    message: &'static str,
) -> Response {
    match serde_json::to_value(result) {
        Ok(structured) => tool_result(id, structured, message),
        Err(_) => tool_error(id, "unavailable", "Maintainer authority is unavailable."),
    }
}

fn operation_error(id: Value, error: OperationError) -> Response {
    match error {
        OperationError::InvalidInput => {
            tool_error(id, "invalid_input", "Operation input is invalid.")
        }
        OperationError::Conflict => tool_error(
            id,
            "conflict",
            "The exact-head or operation binding changed.",
        ),
        OperationError::Indeterminate => tool_error(
            id,
            "indeterminate",
            "The operation outcome is indeterminate and was not repeated.",
        ),
        OperationError::Unavailable => {
            tool_error(id, "unavailable", "Maintainer authority is unavailable.")
        }
    }
}

fn tool_error(id: Value, code: &'static str, message: &'static str) -> Response {
    json_rpc_result(
        id,
        json!({
            "content": [{"type": "text", "text": format!("{code}: {message}")}],
            "isError": true
        }),
    )
}

fn request_id(value: Option<&Value>) -> Option<Value> {
    match value {
        None => None,
        Some(Value::Null) => Some(Value::Null),
        Some(Value::String(value)) if value.len() <= 128 => Some(Value::String(value.clone())),
        Some(Value::Number(value)) if value.is_i64() || value.is_u64() => {
            Some(Value::Number(value.clone()))
        }
        _ => Some(Value::Null),
    }
}

fn single_header<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    let mut values = headers.get_all(name).iter();
    let value = values.next()?;
    if values.next().is_some() {
        return None;
    }
    let value = value.to_str().ok()?;
    (!value.contains(',')).then_some(value)
}

fn json_rpc_result(id: Value, result: Value) -> Response {
    json_response(
        StatusCode::OK,
        json!({"jsonrpc": "2.0", "id": id, "result": result}),
    )
}

fn json_rpc_error(id: Value, code: i64, message: &'static str) -> Response {
    json_response(
        StatusCode::OK,
        json!({"jsonrpc": "2.0", "id": id, "error": {"code": code, "message": message}}),
    )
}

fn json_response(status: StatusCode, body: Value) -> Response {
    (
        status,
        [
            ("content-type", "application/json"),
            ("cache-control", "no-store"),
        ],
        body.to_string(),
    )
        .into_response()
}

fn error_response(status: StatusCode, error: &'static str) -> Response {
    json_response(status, json!({"error": error}))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn protocol_surface_is_stateless_and_typed() {
        let request = json!({"jsonrpc": "2.0", "id": 1, "method": "tools/list"});
        let object = request.as_object().unwrap();
        assert_eq!(
            object.get("method").and_then(Value::as_str),
            Some("tools/list")
        );
        assert_eq!(PROTOCOL_VERSION, "2025-06-18");
        assert_eq!(request_id(object.get("id")), Some(json!(1)));
        assert_eq!(request_id(Some(&json!({"bad": true}))), Some(Value::Null));
        assert_eq!(tools()["tools"][0]["name"], "maintainer_status");
    }
}
