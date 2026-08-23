#![cfg_attr(test, allow(dead_code))]

use std::{collections::BTreeMap, sync::Arc};

use base64::{Engine as _, engine::general_purpose};
use serde::Deserialize;
use zeroize::{Zeroize as _, Zeroizing};

use crate::maintainer::MAX_EXACT_INTEGER;

pub(crate) const PRIVATE_KEY_BINDING: &str = "DARK_FACTORY_MAINTAINER_PRIVATE_KEY_PKCS8";
pub(crate) const PERMISSION_REVISION_BINDING: &str = "DARK_FACTORY_MAINTAINER_PERMISSION_REVISION";
pub(crate) const REPOSITORY_BINDING: &str = "DARK_FACTORY_MAINTAINER_REPOSITORY";
pub(crate) const REPOSITORY_OWNER_ID_BINDING: &str = "DARK_FACTORY_MAINTAINER_REPOSITORY_OWNER_ID";
const PERMISSION_REVISION: &str = "maintainer-metadata-v1";
const GITHUB_API_VERSION: &str = "2026-03-10";
const MAX_GITHUB_RESPONSE_BYTES: usize = 64 * 1024;

#[derive(Clone)]
pub(crate) struct AppAuthority(Arc<Authority>);

struct Authority {
    app_id: i64,
    private_key: PrivateKey,
    repository: RepositoryName,
    repository_owner_id: i64,
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

impl AppAuthority {
    pub(crate) fn new(
        app_id: i64,
        private_key: String,
        permission_revision: String,
        repository: String,
        repository_owner_id: String,
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
        Ok(Self(Arc::new(Authority {
            app_id,
            private_key: PrivateKey(private_key),
            repository,
            repository_owner_id,
        })))
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn verify(&self) -> Result<(), Error> {
        self.0.verify().await
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
        Ok(Self {
            owner: owner.to_owned(),
            name: name.to_owned(),
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
        let jwt = self.jwt().await?;
        let installation: Installation =
            github_json(&self.repository.installation_url(), jwt.as_str()).await?;
        validate_installation(&installation, self.app_id, self.repository_owner_id)
    }

    async fn jwt(&self) -> Result<Credential, Error> {
        let now = (js_sys::Date::now() / 1_000.0).floor() as i64;
        let unsigned = jwt_unsigned(self.app_id, now);
        let signature = sign_rs256(&self.private_key.0, unsigned.as_bytes()).await?;
        Credential::new(format!(
            "{unsigned}.{}",
            general_purpose::URL_SAFE_NO_PAD.encode(signature)
        ))
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

#[derive(Deserialize)]
struct NumericIdentity {
    id: i64,
}

fn validate_installation(
    installation: &Installation,
    app_id: i64,
    owner_id: i64,
) -> Result<(), Error> {
    let expected_permissions = BTreeMap::from([("metadata".to_owned(), "read".to_owned())]);
    if installation.id <= 0
        || installation.app_id != app_id
        || installation.account.id != owner_id
        || installation.repository_selection != "selected"
        || installation.permissions != expected_permissions
        || !installation.events.is_empty()
        || installation.suspended_at.is_some()
    {
        return Err(Error::Unavailable);
    }
    Ok(())
}

#[cfg(target_arch = "wasm32")]
async fn github_json<T: serde::de::DeserializeOwned>(
    url: &str,
    credential: &str,
) -> Result<T, Error> {
    use futures_util::TryStreamExt as _;
    use worker::{Fetch, Headers, Method, Request, RequestInit, RequestRedirect};

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
    let mut init = RequestInit::new();
    init.with_method(Method::Get)
        .with_redirect(RequestRedirect::Error)
        .with_headers(headers)
        .with_body(None);
    let request = Request::new_with_init(url, &init).map_err(|_| Error::Unavailable)?;
    let mut response = Fetch::Request(request)
        .send()
        .await
        .map_err(|_| Error::Unavailable)?;
    if !(200..300).contains(&response.status_code()) {
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
    fn exact_repository_path_and_metadata_only_installation_are_required() {
        let repository = RepositoryName::new("baziyer/dark-factory".into()).unwrap();
        assert_eq!(
            repository.installation_url(),
            "https://api.github.com/repos/baziyer/dark-factory/installation"
        );
        assert!(RepositoryName::new("baziyer/../dark-factory".into()).is_err());
        assert!(RepositoryName::new("baziyer/dark factory".into()).is_err());
        let installation: Installation = serde_json::from_str(
            r#"{"id":17,"app_id":4673420,"account":{"id":109233175},"repository_selection":"selected","permissions":{"metadata":"read"},"events":[],"suspended_at":null}"#,
        )
        .unwrap();
        assert!(validate_installation(&installation, 4_673_420, 109_233_175).is_ok());

        let broader: Installation = serde_json::from_str(
            r#"{"id":17,"app_id":4673420,"account":{"id":109233175},"repository_selection":"selected","permissions":{"contents":"write","metadata":"read"},"events":[],"suspended_at":null}"#,
        )
        .unwrap();
        assert_eq!(
            validate_installation(&broader, 4_673_420, 109_233_175).err(),
            Some(Error::Unavailable)
        );
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
            )
            .err(),
            Some(Error::Configuration)
        );
    }
}
