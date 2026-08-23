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
/// Optional. Absent means no service token may act, which is the correct state
/// for a deployment that has not deliberately enabled headless access.
pub(crate) const SERVICE_TOKEN_BINDING: &str = "DARK_FACTORY_CLOUDFLARE_ACCESS_SERVICE_TOKEN_ID";
const MAX_CERTS_BYTES: usize = 64 * 1024;

#[derive(Clone)]
pub(crate) struct AccessAuthority {
    expected_email_digest: [u8; 32],
    team_domain: String,
    audience: String,
    /// Exact Cloudflare Access service-token client ID permitted to act without
    /// a human identity. `None` rejects every service token, which is the state
    /// a deployment that has not configured one must stay in.
    service_token_id: Option<String>,
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
        service_token_id: Option<String>,
    ) -> Result<Self, Error> {
        let mut digest = [0_u8; 32];
        if !lower_hex(&expected_email_digest, 64)
            || hex::decode_to_slice(expected_email_digest, &mut digest).is_err()
            || !valid_team_domain(&team_domain)
            || !lower_hex(&audience, 64)
        {
            return Err(Error::Configuration);
        }
        // A configured but malformed service-token id is a configuration error,
        // not a silently ignored one: it would otherwise leave the deployment
        // believing headless access is enabled while every call fails closed.
        if let Some(id) = service_token_id.as_deref()
            && !valid_service_token_id(id)
        {
            return Err(Error::Configuration);
        }
        Ok(Self {
            expected_email_digest: digest,
            team_domain,
            audience,
            service_token_id,
        })
    }

    #[cfg(target_arch = "wasm32")]
    pub(crate) async fn authorize(&self, headers: &HeaderMap) -> Result<(), Error> {
        let assertion = single_header(headers, "cf-access-jwt-assertion")?;
        // A human identity carries an authenticated email header; a service
        // token carries none and identifies itself by `common_name` instead.
        // Choosing the principal by header presence keeps each path exact:
        // neither can satisfy the other's identity check.
        let email_header = optional_single_header(headers, "cf-access-authenticated-user-email")?;
        let parsed = ParsedJwt::parse(assertion).inspect_err(|_| {
            // Surface serde's own reason. It names the missing or mistyped
            // claim, which a bare "did not parse" does not, and Cloudflare's
            // claim set differs per principal.
            let reason = assertion
                .split('.')
                .nth(1)
                .and_then(|payload| general_purpose::URL_SAFE_NO_PAD.decode(payload).ok())
                .map(|bytes| match serde_json::from_slice::<Claims>(&bytes) {
                    Ok(_) => "claims parse succeeded on retry".to_owned(),
                    Err(error) => error.to_string(),
                })
                .unwrap_or_else(|| "payload was not decodable base64url".to_owned());
            worker::console_error!("access assertion rejected: {reason}");
        })?;
        let jwk = self
            .signing_key(&parsed.header.kid)
            .await
            .inspect_err(|_| {
                worker::console_error!("access signing key unavailable for kid");
            })?;
        verify_rs256(jwk, parsed.unsigned.as_bytes(), &parsed.signature)
            .await
            .inspect_err(|_| {
                worker::console_error!("access assertion signature did not verify");
            })?;
        self.validate_envelope(&parsed.claims, now())
            .inspect_err(|_| {
                worker::console_error!(
                    "access envelope rejected: kind={} issuer_match={} aud_match={}",
                    parsed.claims.kind,
                    parsed.claims.issuer == self.team_domain,
                    parsed.claims.audience.is_exactly(&self.audience)
                );
            })?;
        match email_header {
            Some(email_header) => self.validate_operator(&parsed.claims, email_header),
            None => self.validate_service_token(&parsed.claims),
        }
    }

    /// Everything both principals must satisfy: this application, this
    /// organization, and a live token.
    fn validate_envelope(&self, claims: &Claims, now: i64) -> Result<(), Error> {
        if claims.kind != "app"
            || claims.issuer != self.team_domain
            || !claims.audience.is_exactly(&self.audience)
            || claims.issued_at > now + 60
            || claims
                .not_before
                .is_some_and(|not_before| not_before > now + 60)
            || claims.expires_at < now - 60
            || claims.expires_at <= claims.issued_at
        {
            return Err(Error::Unauthorized);
        }
        Ok(())
    }

    fn validate_operator(&self, claims: &Claims, email_header: &str) -> Result<(), Error> {
        let email = valid_email(email_header)?;
        let claim_email = valid_email(&claims.email)?;
        let supplied: [u8; 32] = Sha256::digest(email.as_bytes()).into();
        // A service token must never satisfy the operator path, even if Access
        // were ever to attach an email header to one.
        if email != claim_email
            || supplied != self.expected_email_digest
            || !claims.common_name.is_empty()
        {
            return Err(Error::Unauthorized);
        }
        Ok(())
    }

    fn validate_service_token(&self, claims: &Claims) -> Result<(), Error> {
        let expected = self
            .service_token_id
            .as_deref()
            .ok_or(Error::Unauthorized)?;
        // The identity is the client id alone; a service-token assertion that
        // also carried an email would be an unexpected shape, so reject it
        // rather than guess which principal it is.
        if claims.common_name != expected || !claims.email.is_empty() {
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

#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum Audience {
    One(String),
    Many(Vec<String>),
}

impl Audience {
    /// Exactly this audience and no other, whichever shape it arrived in.
    fn is_exactly(&self, expected: &str) -> bool {
        match self {
            Self::One(value) => value == expected,
            Self::Many(values) => values.as_slice() == [expected],
        }
    }
}

#[derive(Deserialize)]
struct Claims {
    // Cloudflare sends `aud` as an array for a human assertion and, for some
    // principals, as a bare string. Accept both and require exactly one value.
    #[serde(rename = "aud")]
    audience: Audience,
    // Absent on a service-token assertion, which has no human identity.
    #[serde(default)]
    email: String,
    // Cloudflare sets this to the service token's client id; empty for a human.
    #[serde(default)]
    common_name: String,
    #[serde(rename = "exp")]
    expires_at: i64,
    #[serde(rename = "iat")]
    issued_at: i64,
    // Cloudflare omits `nbf` from service-token assertions. Enforce it when it
    // is present rather than defaulting it, so a human assertion that carries
    // one is still bound by it.
    #[serde(rename = "nbf", default)]
    not_before: Option<i64>,
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

/// A Cloudflare Access service-token client id: 32 lowercase hex characters
/// followed by `.access`.
fn valid_service_token_id(value: &str) -> bool {
    value
        .strip_suffix(".access")
        .is_some_and(|prefix| lower_hex(prefix, 32))
}

/// Absent is a valid answer; present-but-ambiguous is not. A duplicated or
/// unparsable header still fails closed rather than being treated as missing,
/// which would silently downgrade an operator request to the service path.
#[cfg(target_arch = "wasm32")]
fn optional_single_header<'a>(
    headers: &'a HeaderMap,
    name: &str,
) -> Result<Option<&'a str>, Error> {
    if headers.get(name).is_none() {
        return Ok(None);
    }
    single_header(headers, name).map(Some)
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
            None,
        )
        .unwrap()
    }

    /// The operator path as `authorize` composes it: envelope, then identity.
    fn validate_claims(
        authority: &AccessAuthority,
        claims: &Claims,
        email: &str,
        now: i64,
    ) -> Result<(), Error> {
        authority.validate_envelope(claims, now)?;
        authority.validate_operator(claims, email)
    }

    fn claims() -> Claims {
        Claims {
            audience: Audience::Many(vec!["a".repeat(64)]),
            email: "Operator@Example.com".into(),
            common_name: String::new(),
            expires_at: 1_800_000_600,
            issued_at: 1_800_000_000,
            not_before: Some(1_800_000_000),
            issuer: "https://dark-factory.cloudflareaccess.com".into(),
            kind: "app".into(),
        }
    }

    #[test]
    fn access_claims_bind_issuer_audience_time_and_identity() {
        assert!(
            validate_claims(
                &authority(),
                &claims(),
                "operator@example.com",
                1_800_000_100
            )
            .is_ok()
        );
        let mut wrong = claims();
        wrong.audience = Audience::Many(vec!["b".repeat(64)]);
        assert_eq!(
            validate_claims(&authority(), &wrong, "operator@example.com", 1_800_000_100),
            Err(Error::Unauthorized)
        );
        let mut expired = claims();
        expired.expires_at = 1_799_999_000;
        assert_eq!(
            validate_claims(
                &authority(),
                &expired,
                "operator@example.com",
                1_800_000_100
            ),
            Err(Error::Unauthorized)
        );
        assert_eq!(
            validate_claims(
                &authority(),
                &claims(),
                "attacker@example.com",
                1_800_000_100
            ),
            Err(Error::Unauthorized)
        );
    }

    const SERVICE_TOKEN_ID: &str = "0123456789abcdef0123456789abcdef.access";

    fn service_authority() -> AccessAuthority {
        AccessAuthority::new(
            hex::encode(Sha256::digest(b"operator@example.com")),
            "https://dark-factory.cloudflareaccess.com".into(),
            "a".repeat(64),
            Some(SERVICE_TOKEN_ID.into()),
        )
        .unwrap()
    }

    fn service_claims() -> Claims {
        Claims {
            email: String::new(),
            common_name: SERVICE_TOKEN_ID.into(),
            ..claims()
        }
    }

    #[test]
    fn service_tokens_act_only_when_configured_and_exact() {
        // A headless agent cannot complete Managed OAuth, so it presents a
        // service token: no email header, identity in `common_name`.
        assert!(
            service_authority()
                .validate_envelope(&service_claims(), 1_800_000_100)
                .is_ok()
        );
        assert!(
            service_authority()
                .validate_service_token(&service_claims())
                .is_ok()
        );

        // Absent configuration must reject every service token rather than
        // treat "no expected id" as "any id".
        assert_eq!(
            authority().validate_service_token(&service_claims()),
            Err(Error::Unauthorized)
        );

        let mut other = service_claims();
        other.common_name = "ffffffffffffffffffffffffffffffff.access".into();
        assert_eq!(
            service_authority().validate_service_token(&other),
            Err(Error::Unauthorized)
        );

        // Neither principal may satisfy the other's identity check.
        let mut impersonating = claims();
        impersonating.common_name = SERVICE_TOKEN_ID.into();
        assert_eq!(
            service_authority().validate_operator(&impersonating, "operator@example.com"),
            Err(Error::Unauthorized)
        );
        let mut with_email = service_claims();
        with_email.email = "operator@example.com".into();
        assert_eq!(
            service_authority().validate_service_token(&with_email),
            Err(Error::Unauthorized)
        );

        // The envelope still binds this application and a live token.
        let mut wrong_audience = service_claims();
        wrong_audience.audience = Audience::Many(vec!["b".repeat(64)]);
        assert_eq!(
            service_authority().validate_envelope(&wrong_audience, 1_800_000_100),
            Err(Error::Unauthorized)
        );
    }

    /// The exact claim names Cloudflare sends for a service-token assertion,
    /// captured from a live request: no `nbf` and no `email`. Requiring `nbf`
    /// made deserialisation fail before any identity check ran, so every
    /// headless call was rejected with no indication why.
    #[test]
    fn a_service_token_assertion_deserialises_without_nbf_or_email() {
        let payload = serde_json::json!({
            "aud": ["a".repeat(64)],
            "common_name": SERVICE_TOKEN_ID,
            "exp": 1_800_000_600_i64,
            "h_INTERNAL_DO_NOT_USE": "ignored",
            "iat": 1_799_999_940_i64,
            "iss": "https://dark-factory.cloudflareaccess.com",
            "sub": "",
            "type": "app"
        });
        let claims: Claims =
            serde_json::from_value(payload).expect("service-token claim set must deserialise");
        assert!(claims.not_before.is_none());
        assert!(claims.email.is_empty());
        assert!(
            service_authority()
                .validate_envelope(&claims, 1_800_000_100)
                .is_ok()
        );
        assert!(service_authority().validate_service_token(&claims).is_ok());
        // Cloudflare sends `aud` as a bare string for this principal; requiring
        // an array failed deserialisation exactly as a missing `nbf` did.
        let bare: Claims = serde_json::from_value(serde_json::json!({
            "aud": "a".repeat(64),
            "common_name": SERVICE_TOKEN_ID,
            "exp": 1_800_000_600_i64,
            "iat": 1_799_999_940_i64,
            "iss": "https://dark-factory.cloudflareaccess.com",
            "sub": "",
            "type": "app"
        }))
        .expect("a bare-string audience must deserialise");
        assert!(
            service_authority()
                .validate_envelope(&bare, 1_800_000_100)
                .is_ok()
        );
        let wrong: Claims = serde_json::from_value(serde_json::json!({
            "aud": "b".repeat(64),
            "common_name": SERVICE_TOKEN_ID,
            "exp": 1_800_000_600_i64,
            "iat": 1_799_999_940_i64,
            "iss": "https://dark-factory.cloudflareaccess.com",
            "type": "app"
        }))
        .expect("deserialises");
        assert_eq!(
            service_authority().validate_envelope(&wrong, 1_800_000_100),
            Err(Error::Unauthorized)
        );

        // A present `nbf` is still enforced.
        let mut future = claims;
        future.not_before = Some(1_800_000_600);
        assert_eq!(
            service_authority().validate_envelope(&future, 1_800_000_100),
            Err(Error::Unauthorized)
        );
    }

    #[test]
    fn service_token_identity_is_exact_or_a_configuration_error() {
        assert!(valid_service_token_id(SERVICE_TOKEN_ID));
        for invalid in [
            "",
            ".access",
            "0123456789abcdef0123456789abcdef",
            "0123456789ABCDEF0123456789abcdef.access",
            "0123456789abcdef0123456789abcde.access",
            "0123456789abcdef0123456789abcdeff.access",
            "0123456789abcdef0123456789abcdef.access.access",
        ] {
            assert!(!valid_service_token_id(invalid), "accepted: {invalid}");
        }
        // Misconfiguration must fail loudly at construction, not silently
        // disable headless access at request time.
        assert_eq!(
            AccessAuthority::new(
                hex::encode(Sha256::digest(b"operator@example.com")),
                "https://dark-factory.cloudflareaccess.com".into(),
                "a".repeat(64),
                Some("not-a-service-token".into()),
            )
            .err(),
            Some(Error::Configuration)
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
                None,
            )
            .is_ok()
        );
        assert!(
            AccessAuthority::new(
                "a".repeat(64),
                "https://dark-factory.cloudflareaccess.com".into(),
                "B".repeat(64),
                None,
            )
            .is_err()
        );
    }
}
