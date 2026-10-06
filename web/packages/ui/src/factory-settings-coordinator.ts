import {
  CAPABILITIES,
  type BrowserClientsView,
  type BrowserSession,
  type DiscoveredAccountView,
  type RepositoryMutation,
  type RepositoryView,
  type IntakeView,
  type GitHubConnectionResult,
} from "@dark-factory/client";

const LOOPBACK_GRANT = CAPABILITIES.human_actions | CAPABILITIES.terminal_input;

export type FactoryRemoteInvite = Readonly<{
  link: string;
  svg: string;
  expiresAtMs: bigint;
}>;

export type FactoryGitHubView = Readonly<{ result?: GitHubConnectionResult; pending: boolean; error?: string }>;

type SettingsSession = Pick<BrowserSession, "discoverAccounts" | "linkAccount" | "updateAccount" | "inviteRemote" | "listBrowserClients" | "revokeBrowserClient" | "githubConnection" | "intake" | "getRepositories" | "mutateRepository" | "createProject" | "capabilities" | "clientId">;

type SettingsOwner = Readonly<{
  session(): SettingsSession | undefined;
  ready(): boolean;
  generation(): number;
  current(generation: number): boolean;
  errorCode(error: unknown): string;
  publish(): void;
}>;

type Observation = "accounts" | "devices" | "github" | "invite" | "repositories" | "intake";

type ObserveOptions = Readonly<{
  /** Replies count only for the same ready session, not just the same generation. */
  scoped?: boolean;
  /** Release pending even when the reply is stale. */
  release?: boolean;
  /** Publish only once the reply settles. */
  quiet?: boolean;
  /** Run alongside an observation already pending under the same key. */
  overlap?: boolean;
  failed?: () => void;
}>;

/** Owns SETTINGS-only observations and remote invitation state. */
export class FactorySettingsCoordinator {
  readonly #owner: SettingsOwner;
  readonly #pending = observations(() => new Set<string>());
  readonly #errors = observations(() => new Map<string, string>());
  #remoteInvite: FactoryRemoteInvite | undefined;
  #accounts: readonly DiscoveredAccountView[] | undefined;
  #devices: BrowserClientsView | undefined;
  #repositories = new Map<string, readonly RepositoryView[]>();
  #intake = new Map<string, IntakeView>();
  #repositoryMutationErrors = new Set<string>();
  #github: GitHubConnectionResult | undefined;
  #githubStatusQueued = false;

  constructor(owner: SettingsOwner) {
    this.#owner = owner;
  }

  get remoteInvite(): FactoryRemoteInvite | undefined { return this.#remoteInvite; }
  get remoteInviteError(): string | undefined { return this.#errors.invite.get(""); }
  get remoteInviteAllowed(): boolean {
    return this.#owner.ready() && ((this.#owner.session()?.capabilities ?? 0) & LOOPBACK_GRANT) === LOOPBACK_GRANT;
  }
  get accounts(): readonly DiscoveredAccountView[] | undefined { return this.#accounts; }
  get accountsPending(): boolean { return this.#pending.accounts.has(""); }
  get accountsError(): string | undefined { return this.#errors.accounts.get(""); }
  /** The identities the factory has granted, once SETTINGS has asked. */
  get devices(): BrowserClientsView | undefined { return this.#devices; }
  get devicesError(): string | undefined { return this.#errors.devices.get(""); }
  get ownClientId(): string | undefined { return this.#owner.session()?.clientId; }
  get repositories(): ReadonlyMap<string, readonly RepositoryView[]> { return this.#repositories; }
  get repositoryPending(): ReadonlySet<string> { return this.#pending.repositories; }
  get repositoryErrors(): ReadonlyMap<string, string> { return this.#errors.repositories; }
  get intake(): ReadonlyMap<string, IntakeView> { return this.#intake; }
  get intakePending(): ReadonlySet<string> { return this.#pending.intake; }
  get intakeErrors(): ReadonlyMap<string, string> { return this.#errors.intake; }

  /** Private checkout paths and issue text belong to the current session. */
  clearProjectSettings(): void {
    this.#repositories.clear(); this.#pending.repositories.clear(); this.#errors.repositories.clear(); this.#repositoryMutationErrors.clear();
    this.#intake.clear(); this.#pending.intake.clear(); this.#errors.intake.clear();
  }

  #current(generation: number, session: SettingsSession): boolean {
    return this.#owner.current(generation) && this.#owner.ready() && this.#owner.session() === session;
  }

  /**
   * Runs one observation under `key`'s pending flag and records its error.
   * Resolves true once it succeeds, false once it fails, and undefined when it
   * was refused or its reply arrived for a replaced session.
   */
  async #observe([kind, id = ""]: readonly [Observation, string?], run: (session: SettingsSession, current: () => boolean) => Promise<unknown>, options: ObserveOptions = {}): Promise<boolean | undefined> {
    const session = this.#owner.session();
    const pending = this.#pending[kind], errors = this.#errors[kind];
    if (!this.#owner.ready() || session === undefined || (!options.overlap && pending.has(id))) return undefined;
    const generation = this.#owner.generation();
    const current = options.scoped ? () => this.#current(generation, session) : () => this.#owner.current(generation);
    let outcome: boolean;
    pending.add(id);
    if (!options.quiet) this.#owner.publish();
    try {
      await run(session, current);
      if (!current()) return undefined;
      if (kind !== "repositories" || !this.#repositoryMutationErrors.has(id)) errors.delete(id);
      outcome = true;
    } catch (error) {
      if (!current()) return undefined;
      options.failed?.();
      errors.set(id, this.#owner.errorCode(error));
      outcome = false;
    } finally {
      if (options.release || current()) pending.delete(id);
    }
    this.#owner.publish();
    return outcome;
  }

  async loadIntake(projectId: string): Promise<void> {
    await this.#observe(["intake", projectId], async (session, current) => {
      const result = await session.intake({ action: "list", project_id: projectId });
      if (current()) this.#intake.set(projectId, result);
    }, { scoped: true });
  }

  async intakeAction(projectId: string, request: Parameters<BrowserSession["intake"]>[0]): Promise<void> {
    if (!this.#owner.ready() || this.#owner.session() === undefined) return;
    if (["preview","refresh","accept","update"].includes(request.action)) {
      const prior = this.#intake.get(projectId);
      if (prior !== undefined) this.#intake.set(projectId, { ...prior, candidates: undefined, reviewed_revision: undefined, next_page: undefined });
    }
    await this.#observe(["intake", projectId], async (session, current) => {
      let result = await session.intake(request);
      if (!current()) return;
      if (request.action === "accept" && result.state === "accepted" && result.acceptance_id !== undefined) result = await session.intake({action:"import", acceptance_id:result.acceptance_id});
      if (!current()) return;
      const prior = this.#intake.get(projectId);
      const sources = result.sources === undefined ? prior?.sources : request.action === "list" || prior?.sources === undefined ? result.sources : mergeIntakeSources(prior.sources, result.sources);
      this.#intake.set(projectId, Object.freeze({
        ...prior,
        ...result,
        ...(request.action === "linear_disconnect" ? {linear_teams:undefined}:{}),
        ...(sources === undefined ? {} : { sources }),
        ...(result.candidates === undefined && !["update", "accept", "preview", "refresh"].includes(request.action) ? prior?.candidates === undefined ? {} : { candidates: prior.candidates } : {}),
        ...(request.action === "withdraw" && (result.state === "withdrawn" || result.state === "withdrawal_pending") ? { candidates: prior?.candidates?.map((candidate) => candidate.acceptance_id === request.acceptance_id ? { ...candidate, reason: result.state } : candidate) } : {}),
        ...(["update", "accept"].includes(request.action) || (["preview", "refresh"].includes(request.action) && result.state !== "ok") ? { candidates: undefined, reviewed_revision: undefined, next_page: undefined } : {}),
        ...(result.imported_tasks === undefined ? { imported_tasks: [] } : {}),
      }));
      if (request.action === "accept" && result.state === "imported" && request.source_id !== undefined) {
        const preview = await session.intake({action:"preview",source_id:request.source_id,page:1});
        if (current()) this.#intake.set(projectId,{...this.#intake.get(projectId),...preview});
      }
    }, { scoped: true, overlap: true });
  }
  get github(): FactoryGitHubView { return { result: this.#github, pending: this.#pending.github.has(""), error: this.#errors.github.get("") }; }
  /** A replacement browser session must rediscover private GitHub state. */
  clearGitHub(): void {
    if (this.#github === undefined && this.#errors.github.size === 0) return;
    this.#github = undefined;
    this.#errors.github.clear();
    this.#owner.publish();
  }

  clearRemoteInvite(): void {
    this.#remoteInvite = undefined;
    this.#errors.invite.clear();
  }

  async githubConnection(request: Parameters<BrowserSession["githubConnection"]>[0]): Promise<void> {
    if (this.#owner.ready() && this.#owner.session() !== undefined && this.#pending.github.has("")) {
      if (request.action === "status") this.#githubStatusQueued = true;
      return;
    }
    const outcome = await this.#observe(["github"], async (session, current) => {
      const result = await session.githubConnection(request);
      if (!current()) return;
      if (request.action === "disconnect" && result.state === "ok") {
        this.#github = { state: "disconnected" };
      } else if (request.action === "connect" || result.state !== "ok" || this.#github === undefined) {
        const previousAuthorization = this.#github?.authorization;
        const preserveAuthorization = (request.action === "confirm" || request.action === "status" || request.action === "refresh") && previousAuthorization !== undefined;
        this.#github = !preserveAuthorization ? result : { ...result, authorization: previousAuthorization };
      } else if (result.state === "ok" && this.#github !== undefined) {
        const sameConnection = result.status === undefined || this.#github.status?.connection_id === result.status.connection_id;
        this.#github = {
          ...this.#github,
          ...result,
          authorization: result.authorization ?? (result.status?.state === "pending" || result.status?.state === "awaiting_confirmation" ? this.#github.authorization : undefined),
          status: result.status ?? this.#github.status,
          installations: request.action === "refresh" || request.action === "status" ? result.installations : sameConnection ? result.installations ?? this.#github.installations : result.installations,
          repositories: request.action === "refresh" || request.action === "status" ? result.repositories : sameConnection ? result.repositories ?? this.#github.repositories : result.repositories,
        };
      }
    });
    if (outcome === undefined) return;
    if (this.#githubStatusQueued && request.action !== "status") {
      this.#githubStatusQueued = false;
      await this.githubConnection({ action: "status" });
      return;
    }
    if (request.action === "confirm" && this.#github?.state === "ok" || (request.action === "refresh" || request.action === "status") && this.#github?.status?.state === "connected") {
      if (request.action === "confirm") await this.githubConnection({ action: "refresh" });
      else await this.githubConnection({ action: "installations", page: 1 });
    }
  }

  async loadAccounts(): Promise<void> {
    await this.#observe(["accounts"], async (session) => { this.#accounts = await session.discoverAccounts(); });
  }

  linkAccount(request: Parameters<BrowserSession["linkAccount"]>[0]): Promise<void> {
    return this.#changeAccount(request);
  }

  updateAccount(request: Parameters<BrowserSession["updateAccount"]>[0]): Promise<void> {
    return this.#changeAccount(request);
  }

  async loadRepositories(projectId: string): Promise<void> {
    await this.#observe(["repositories", projectId], async (session, current) => {
      const repositories = await session.getRepositories(projectId);
      if (current()) this.#repositories.set(projectId, repositories);
    }, { scoped: true, release: true });
  }

  async mutateRepository(request: RepositoryMutation): Promise<void> {
    const owned = this.#owner.session();
    if (owned === undefined) return;
    const generation = this.#owner.generation();
    let added: RepositoryView | undefined;
    const outcome = await this.#observe(["repositories", request.projectId], async (session, current) => {
      const result = await session.mutateRepository(request);
      if (!current()) return;
      if (request.action === "add") added = result;
      if (result !== undefined && (request.action === "fetch" || request.action === "github")) {
        this.#repositories.set(request.projectId, (this.#repositories.get(request.projectId) ?? []).map((item) => item.id !== result.id ? item : item.revision !== result.revision ? result : { ...result,
          ...(request.action === "fetch" ? { publication_state: item.publication_state } : { fetch_state: item.fetch_state }),
        }));
      }
      this.#repositoryMutationErrors.delete(request.projectId);
    }, { scoped: true, release: true, failed: () => this.#repositoryMutationErrors.add(request.projectId) });
    if (outcome === undefined || !this.#current(generation, owned)) return;
    if (request.action !== "fetch" && request.action !== "github") await this.loadRepositories(request.projectId);
    if (added !== undefined) {
      for (const action of ["fetch", "github"] as const) {
        if (!this.#current(generation, owned)) return;
        await this.mutateRepository({ action, projectId: request.projectId, repositoryId: added.id });
      }
    }
  }

  async createProject(request: { name: string; root: string }): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined) return;
    const generation = this.#owner.generation();
    try {
      await session.createProject(request);
      if (!this.#current(generation, session)) return;
      this.#errors.repositories.delete("create");
    } catch (error) {
      if (!this.#current(generation, session)) return;
      this.#errors.repositories.set("create", this.#owner.errorCode(error));
    }
    this.#owner.publish();
  }

  async #changeAccount(request: Parameters<BrowserSession["linkAccount"]>[0] | Parameters<BrowserSession["updateAccount"]>[0]): Promise<void> {
    const changed = await this.#observe(["accounts"], async (session) => {
      if ("accountId" in request) await session.updateAccount(request);
      else await session.linkAccount(request);
    });
    if (changed) await this.loadAccounts();
  }

  async loadDevices(): Promise<void> {
    await this.#observe(["devices"], async (session) => { this.#devices = await session.listBrowserClients(); }, { quiet: true });
  }

  /** Withdraws one identity, then rereads the list so it is gone from it. */
  async revokeDevice(request: { clientId: string; expectedRevision: bigint }): Promise<void> {
    if (await this.#observe(["devices"], (session) => session.revokeBrowserClient(request), { quiet: true })) await this.loadDevices();
  }

  async inviteRemote(): Promise<void> {
    await this.#observe(["invite"], async (session, current) => {
      const invite = await session.inviteRemote();
      if (current()) this.#remoteInvite = { link: invite.link, svg: invite.svg, expiresAtMs: invite.expiresAtMs };
    }, { quiet: true, release: true, failed: () => { this.#remoteInvite = undefined; } });
  }

  dismissRemoteInvite(): void {
    this.#remoteInvite = undefined;
    this.#errors.invite.clear();
    this.#owner.publish();
  }
}

function observations<T>(create: () => T): Readonly<Record<Observation, T>> {
  return { accounts: create(), devices: create(), github: create(), invite: create(), repositories: create(), intake: create() };
}

function mergeIntakeSources(current: readonly import("@dark-factory/client").IntakeSource[], changed: readonly import("@dark-factory/client").IntakeSource[]): import("@dark-factory/client").IntakeSource[] {
  const replacement = new Map(changed.map((source) => [source.id, source]));
  return [...current.map((source) => replacement.get(source.id) ?? source), ...changed.filter((source) => !current.some((existing) => existing.id === source.id))];
}
