#![cfg_attr(test, allow(dead_code))]

#[cfg(target_arch = "wasm32")]
use axum::http::HeaderMap;
#[cfg(target_arch = "wasm32")]
use base64::{Engine as _, engine::general_purpose};
use serde::Deserialize;
use sha2::{Digest as _, Sha256};

pub(crate) const OPERATOR_EMAIL_DIGEST_BINDING: &str =
    "DARK_FACTORY_MAINTAINER_OPERATOR_EMAIL_SHA256";
pub(crate) const TEAM_DOMAIN_BINDING: &str = "DARK_FACTORY_CLOUDFLARE_ACCESS_TEAM_DOMAIN";
pub(crate) const AUDIENCE_BINDING: &str = "DARK_FACTORY_CLOUDFLARE_ACCESS_AUD";
const MAX_CERTS_BYTES: usize = 64 * 1024;

#[derive(Clone)]
pub(crate) struct AccessAuthority {
    expected_email_digest: [u8; 32],
    team_domain: String,
    audience: String,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, thiserror::Error)]
pub(crate) enum Error {
    #[error("Cloudflare Access authority configuration is invalid")]
    Configuration,
    #[error("Cloudflare Access identity is invalid")]
    Unauthorized,
}

impl AccessAuthority {
    pub(crate) fn new(
        expected_email_digest: String,
        team_domain: String,
        audience: String,
    ) -> Result<Self, Error> {
        let mut digest = [0_u8; 32];
        if !lower_hex(&expected_email_digest, 64)
            || hex::decode_to_slice(expected_email_digest, &mut digest).is_err()
            || !valid_team_domain(&team_domain)
            || !lower_hex(&audience, 64)
        {
            return Err(Error::Configuration);
        }
        Ok(Self {
            expected_email_digest: digest,
            team_domain,
            audience,
        })
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn authorize(&self, headers: &HeaderMap) -> Result<(), Error> {
        let assertion = single_header(headers, "cf-access-jwt-assertion")?;
        let email_header = single_header(headers, "cf-access-authenticated-user-email")?;
        let parsed = ParsedJwt::parse(assertion)?;
        let jwk = self.signing_key(&parsed.header.kid).await?;
        verify_rs256(jwk, parsed.unsigned.as_bytes(), &parsed.signature).await?;
        self.validate_claims(&parsed.claims, email_header, now())
    }

    fn validate_claims(&self, claims: &Claims, email_header: &str, now: i64) -> Result<(), Error> {
        let email = valid_email(email_header)?;
        let claim_email = valid_email(&claims.email)?;
        let supplied: [u8; 32] = Sha256::digest(email.as_bytes()).into();
        if email != claim_email
            || supplied != self.expected_email_digest
            || claims.kind != "app"
            || claims.issuer != self.team_domain
            || claims.audience.as_slice() != [self.audience.as_str()]
            || claims.issued_at > now + 60
            || claims.not_before > now + 60
            || claims.expires_at < now - 60
            || claims.expires_at <= claims.issued_at
        {
            return Err(Error::Unauthorized);
        }
        Ok(())
    }

    /// Prove the live dependency the MCP surface actually has.
    ///
    /// Readiness previously reported the operations surface ready whenever it
    /// was merely configured, so a Worker that could not reach Cloudflare
    /// Access at all still advertised `mcp_pr_create_review_checks` while every
    /// authenticated call returned 401. Fetch the signing keys and require one
    /// usable RS256 key, which is the same document `authorize` depends on.
    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn ready(&self) -> Result<(), Error> {
        let keys = self.certs().await.inspect_err(|_| {
            worker::console_error!("readiness: cloudflare access signing keys unavailable");
        })?;
        keys.keys
            .iter()
            .any(|key| {
                key.kind == "RSA"
                    && key.algorithm == "RS256"
                    && key.usage == "sig"
                    && usable_modulus(&key.modulus)
                    && key.exponent == "AQAB"
            })
            .then_some(())
            .ok_or(Error::Unauthorized)
            .inspect_err(|_| {
                worker::console_error!("readiness: no usable cloudflare access signing key");
            })
    }

    #[cfg(target_arch = "wasm32")]
    async fn signing_key(&self, kid: &str) -> Result<JsonWebKey, Error> {
        let keys = self.certs().await?;
        let mut matches = keys.keys.into_iter().filter(|key| {
            key.kid == kid && key.kind == "RSA" && key.algorithm == "RS256" && key.usage == "sig"
        });
        let key = matches.next().ok_or(Error::Unauthorized)?;
        if matches.next().is_some() || !usable_modulus(&key.modulus) || key.exponent != "AQAB" {
            return Err(Error::Unauthorized);
        }
        Ok(key)
    }

    #[cfg(target_arch = "wasm32")]
    async fn certs(&self) -> Result<JsonWebKeySet, Error> {
        use futures_util::TryStreamExt as _;
        use worker::{Fetch, Headers, Method, Request, RequestInit, RequestRedirect};

        let headers = Headers::new();
        headers
            .set("accept", "application/json")
            .map_err(|_| Error::Unauthorized)?;
        let mut init = RequestInit::new();
        init.with_method(Method::Get)
            // Workers' Request accepts only `follow` and `manual`; `error` makes the
            // constructor throw, so the signing key could never be fetched and every
            // authenticated MCP call failed closed. The 200-only check below still
            // rejects a redirect rather than following it.
            .with_redirect(RequestRedirect::Manual)
            .with_headers(headers)
            .with_body(None);
        let request =
            Request::new_with_init(&format!("{}/cdn-cgi/access/certs", self.team_domain), &init)
                .map_err(|_| Error::Unauthorized)?;
        let mut response = Fetch::Request(request)
            .send()
            .await
            .map_err(|_| Error::Unauthorized)?;
        if response.status_code() != 200 {
            return Err(Error::Unauthorized);
        }
        let mut stream = response.stream().map_err(|_| Error::Unauthorized)?;
        let mut bytes = Vec::new();
        while let Some(mut chunk) = stream.try_next().await.map_err(|_| Error::Unauthorized)? {
            if bytes
                .len()
                .checked_add(chunk.len())
                .is_none_or(|length| length > MAX_CERTS_BYTES)
            {
                return Err(Error::Unauthorized);
            }
            bytes.append(&mut chunk);
        }
        serde_json::from_slice(&bytes).map_err(|_| Error::Unauthorized)
    }
}

/// The bounds `signing_key` has always enforced, shared so readiness proves the
/// same key shape it will later verify against rather than a weaker one.
#[cfg(target_arch = "wasm32")]
fn usable_modulus(modulus: &str) -> bool {
    (342..=1_024).contains(&modulus.len())
        && general_purpose::URL_SAFE_NO_PAD.decode(modulus).is_ok()
}

#[cfg(target_arch = "wasm32")]
struct ParsedJwt {
    header: JwtHeader,
    claims: Claims,
    unsigned: String,
    signature: Vec<u8>,
}

#[cfg(target_arch = "wasm32")]
impl ParsedJwt {
    fn parse(value: &str) -> Result<Self, Error> {
        if !(128..=16_384).contains(&value.len()) {
            return Err(Error::Unauthorized);
        }
        let segments = value.split('.').collect::<Vec<_>>();
        let [header, claims, signature] = segments.as_slice() else {
            return Err(Error::Unauthorized);
        };
        if !segments.iter().all(|segment| {
            !segment.is_empty()
                && segment
                    .bytes()
                    .all(|byte| byte.is_ascii_alphanumeric() || byte == b'-' || byte == b'_')
        }) {
            return Err(Error::Unauthorized);
        }
        let header: JwtHeader = decode_json(header)?;
        if header.algorithm != "RS256" || header.kind != "JWT" || !lower_hex(&header.kid, 64) {
            return Err(Error::Unauthorized);
        }
        Ok(Self {
            header,
            claims: decode_json(claims)?,
            unsigned: format!("{}.{}", segments[0], segments[1]),
            signature: general_purpose::URL_SAFE_NO_PAD
                .decode(signature)
                .map_err(|_| Error::Unauthorized)?,
        })
    }
}

#[cfg(target_arch = "wasm32")]
fn decode_json<T: serde::de::DeserializeOwned>(value: &str) -> Result<T, Error> {
    let bytes = general_purpose::URL_SAFE_NO_PAD
        .decode(value)
        .map_err(|_| Error::Unauthorized)?;
    serde_json::from_slice(&bytes).map_err(|_| Error::Unauthorized)
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct JwtHeader {
    #[serde(rename = "alg")]
    algorithm: String,
    kid: String,
    #[serde(rename = "typ")]
    kind: String,
}

#[derive(Deserialize)]
struct Claims {
    #[serde(rename = "aud")]
    audience: Vec<String>,
    email: String,
    #[serde(rename = "exp")]
    expires_at: i64,
    #[serde(rename = "iat")]
    issued_at: i64,
    #[serde(rename = "nbf")]
    not_before: i64,
    #[serde(rename = "iss")]
    issuer: String,
    #[serde(rename = "type")]
    kind: String,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct JsonWebKeySet {
    keys: Vec<JsonWebKey>,
}

#[cfg(target_arch = "wasm32")]
#[derive(Deserialize)]
struct JsonWebKey {
    #[serde(rename = "kty")]
    kind: String,
    #[serde(rename = "alg")]
    algorithm: String,
    #[serde(rename = "use")]
    usage: String,
    kid: String,
    #[serde(rename = "n")]
    modulus: String,
    #[serde(rename = "e")]
    exponent: String,
}

fn lower_hex(value: &str, length: usize) -> bool {
    value.len() == length
        && value
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase())
}

fn valid_team_domain(value: &str) -> bool {
    let Some(team) = value
        .strip_prefix("https://")
        .and_then(|value| value.strip_suffix(".cloudflareaccess.com"))
    else {
        return false;
    };
    !team.is_empty()
        && team.len() <= 63
        && !team.starts_with('-')
        && !team.ends_with('-')
        && team
            .bytes()
            .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || byte == b'-')
}

fn valid_email(value: &str) -> Result<String, Error> {
    if value.len() < 3
        || value.len() > 320
        || !value.is_ascii()
        || value.bytes().any(|byte| byte.is_ascii_whitespace())
        || value.matches('@').count() != 1
    {
        return Err(Error::Unauthorized);
    }
    Ok(value.to_ascii_lowercase())
}

#[cfg(target_arch = "wasm32")]
fn single_header<'a>(headers: &'a HeaderMap, name: &str) -> Result<&'a str, Error> {
    let mut values = headers.get_all(name).iter();
    let value = values.next().ok_or(Error::Unauthorized)?;
    if values.next().is_some() {
        return Err(Error::Unauthorized);
    }
    let value = value.to_str().map_err(|_| Error::Unauthorized)?;
    if value.contains(',') {
        return Err(Error::Unauthorized);
    }
    Ok(value)
}

#[cfg(target_arch = "wasm32")]
fn now() -> i64 {
    (js_sys::Date::now() / 1_000.0).floor() as i64
}

#[cfg(target_arch = "wasm32")]
async fn verify_rs256(key: JsonWebKey, message: &[u8], signature: &[u8]) -> Result<(), Error> {
    use js_sys::{Array, Function, Object, Promise, Reflect, Uint8Array};
    use wasm_bindgen::{JsCast as _, JsValue};
    use wasm_bindgen_futures::JsFuture;

    let algorithm = Object::new();
    Reflect::set(&algorithm, &"name".into(), &"RSASSA-PKCS1-v1_5".into())
        .map_err(|_| Error::Unauthorized)?;
    Reflect::set(&algorithm, &"hash".into(), &"SHA-256".into()).map_err(|_| Error::Unauthorized)?;
    let jwk = Object::new();
    for (name, value) in [
        ("kty", key.kind),
        ("alg", key.algorithm),
        ("use", key.usage),
        ("kid", key.kid),
        ("n", key.modulus),
        ("e", key.exponent),
    ] {
        Reflect::set(&jwk, &name.into(), &value.into()).map_err(|_| Error::Unauthorized)?;
    }
    let global = js_sys::global();
    let crypto = Reflect::get(&global, &"crypto".into()).map_err(|_| Error::Unauthorized)?;
    let subtle = Reflect::get(&crypto, &"subtle".into()).map_err(|_| Error::Unauthorized)?;
    let import_key: Function = Reflect::get(&subtle, &"importKey".into())
        .map_err(|_| Error::Unauthorized)?
        .dyn_into()
        .map_err(|_| Error::Unauthorized)?;
    let usages = Array::new();
    usages.push(&"verify".into());
    let promise: Promise = import_key
        .call5(
            &subtle,
            &"jwk".into(),
            jwk.as_ref(),
            algorithm.as_ref(),
            &JsValue::FALSE,
            usages.as_ref(),
        )
        .map_err(|_| Error::Unauthorized)?
        .dyn_into()
        .map_err(|_| Error::Unauthorized)?;
    let imported = JsFuture::from(promise)
        .await
        .map_err(|_| Error::Unauthorized)?;
    let verify: Function = Reflect::get(&subtle, &"verify".into())
        .map_err(|_| Error::Unauthorized)?
        .dyn_into()
        .map_err(|_| Error::Unauthorized)?;
    let signature = Uint8Array::from(signature);
    let message = Uint8Array::from(message);
    let promise: Promise = verify
        .call4(
            &subtle,
            &"RSASSA-PKCS1-v1_5".into(),
            &imported,
            signature.as_ref(),
            message.as_ref(),
        )
        .map_err(|_| Error::Unauthorized)?
        .dyn_into()
        .map_err(|_| Error::Unauthorized)?;
    let verified = JsFuture::from(promise)
        .await
        .map_err(|_| Error::Unauthorized)?
        .as_bool()
        .ok_or(Error::Unauthorized)?;
    verified.then_some(()).ok_or(Error::Unauthorized)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn authority() -> AccessAuthority {
        AccessAuthority::new(
            hex::encode(Sha256::digest(b"operator@example.com")),
            "https://dark-factory.cloudflareaccess.com".into(),
            "a".repeat(64),
        )
        .unwrap()
    }

    fn claims() -> Claims {
        Claims {
            audience: vec!["a".repeat(64)],
            email: "Operator@Example.com".into(),
            expires_at: 1_800_000_600,
            issued_at: 1_800_000_000,
            not_before: 1_800_000_000,
            issuer: "https://dark-factory.cloudflareaccess.com".into(),
            kind: "app".into(),
        }
    }

    #[test]
    fn access_claims_bind_issuer_audience_time_and_identity() {
        assert!(
            authority()
                .validate_claims(&claims(), "operator@example.com", 1_800_000_100)
                .is_ok()
        );
        let mut wrong = claims();
        wrong.audience = vec!["b".repeat(64)];
        assert_eq!(
            authority().validate_claims(&wrong, "operator@example.com", 1_800_000_100),
            Err(Error::Unauthorized)
        );
        let mut expired = claims();
        expired.expires_at = 1_799_999_000;
        assert_eq!(
            authority().validate_claims(&expired, "operator@example.com", 1_800_000_100),
            Err(Error::Unauthorized)
        );
        assert_eq!(
            authority().validate_claims(&claims(), "attacker@example.com", 1_800_000_100),
            Err(Error::Unauthorized)
        );
    }

    #[test]
    fn access_configuration_is_exact() {
        assert!(valid_team_domain(
            "https://dark-factory.cloudflareaccess.com"
        ));
        for invalid in [
            "http://dark-factory.cloudflareaccess.com",
            "https://dark-factory.cloudflareaccess.com/path",
            "https://evil.example.com",
            "https://UPPER.cloudflareaccess.com",
        ] {
            assert!(!valid_team_domain(invalid));
        }
        assert!(
            AccessAuthority::new(
                "a".repeat(64),
                "https://dark-factory.cloudflareaccess.com".into(),
                "b".repeat(64),
            )
            .is_ok()
        );
        assert!(
            AccessAuthority::new(
                "a".repeat(64),
                "https://dark-factory.cloudflareaccess.com".into(),
                "B".repeat(64),
            )
            .is_err()
        );
    }
}
