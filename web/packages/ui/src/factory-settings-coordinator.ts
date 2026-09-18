import {
  CAPABILITIES,
  type BrowserClientsView,
  type BrowserSession,
  type DiscoveredAccountView,
  type GitHubConnectionResult,
  type RepositoryMutation,
  type RepositoryView,
} from "@dark-factory/client";

const LOOPBACK_GRANT = CAPABILITIES.human_actions | CAPABILITIES.terminal_input;

export type FactoryRemoteInvite = Readonly<{
  link: string;
  svg: string;
  expiresAtMs: bigint;
}>;

export type FactoryGitHubView = Readonly<{ result?: GitHubConnectionResult; pending: boolean; error?: string }>;

type SettingsSession = Pick<BrowserSession, "discoverAccounts" | "linkAccount" | "updateAccount" | "inviteRemote" | "listBrowserClients" | "revokeBrowserClient" | "githubConnection" | "getRepositories" | "mutateRepository" | "createProject" | "capabilities" | "clientId">;

type SettingsOwner = Readonly<{
  session(): SettingsSession | undefined;
  ready(): boolean;
  generation(): number;
  current(generation: number): boolean;
  errorCode(error: unknown): string;
  publish(): void;
}>;

/** Owns SETTINGS-only observations and remote invitation state. */
export class FactorySettingsCoordinator {
  readonly #owner: SettingsOwner;
  #remoteInvite: FactoryRemoteInvite | undefined;
  #remoteInviteError: string | undefined;
  #remoteInvitePending = false;
  #accounts: readonly DiscoveredAccountView[] | undefined;
  #accountsPending = false;
  #accountsError: string | undefined;
  #devices: BrowserClientsView | undefined;
  #devicesPending = false;
  #devicesError: string | undefined;
  #repositories = new Map<string, readonly RepositoryView[]>();
  #repositoryPending = new Set<string>();
  #repositoryErrors = new Map<string, string>();
  #repositoryMutationErrors = new Set<string>();
  #github: GitHubConnectionResult | undefined;
  #githubPending = false;
  #githubError: string | undefined;

  constructor(owner: SettingsOwner) {
    this.#owner = owner;
  }

  get remoteInvite(): FactoryRemoteInvite | undefined { return this.#remoteInvite; }
  get remoteInviteError(): string | undefined { return this.#remoteInviteError; }
  get remoteInviteAllowed(): boolean {
    return this.#owner.ready() && ((this.#owner.session()?.capabilities ?? 0) & LOOPBACK_GRANT) === LOOPBACK_GRANT;
  }
  get accounts(): readonly DiscoveredAccountView[] | undefined { return this.#accounts; }
  get accountsPending(): boolean { return this.#accountsPending; }
  get accountsError(): string | undefined { return this.#accountsError; }
  /** The identities the factory has granted, once SETTINGS has asked. */
  get devices(): BrowserClientsView | undefined { return this.#devices; }
  get devicesError(): string | undefined { return this.#devicesError; }
  get ownClientId(): string | undefined { return this.#owner.session()?.clientId; }
  get repositories(): ReadonlyMap<string, readonly RepositoryView[]> { return this.#repositories; }
  get repositoryPending(): ReadonlySet<string> { return this.#repositoryPending; }
  get repositoryErrors(): ReadonlyMap<string, string> { return this.#repositoryErrors; }
  get github(): FactoryGitHubView { return { result: this.#github, pending: this.#githubPending, error: this.#githubError }; }

  /** A replacement browser session must rediscover private GitHub state. */
  clearGitHub(): void {
    if (this.#github === undefined && this.#githubError === undefined) return;
    this.#github = undefined;
    this.#githubError = undefined;
    this.#owner.publish();
  }

  async githubConnection(request: Parameters<BrowserSession["githubConnection"]>[0]): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined || this.#githubPending) return;
    const generation = this.#owner.generation();
    this.#githubPending = true;
    this.#owner.publish();
    try {
      const result = await session.githubConnection(request);
      if (!this.#owner.current(generation)) return;
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
          installations: request.action === "refresh" ? result.installations : sameConnection ? result.installations ?? this.#github.installations : result.installations,
          repositories: request.action === "refresh" ? result.repositories : sameConnection ? result.repositories ?? this.#github.repositories : result.repositories,
        };
      }
      this.#githubError = undefined;
    } catch (error) {
      if (!this.#owner.current(generation)) return;
      this.#githubError = this.#owner.errorCode(error);
    } finally {
      if (this.#owner.current(generation)) this.#githubPending = false;
    }
    this.#owner.publish();
    if (request.action === "confirm" && this.#github?.state === "ok" || request.action === "refresh" && this.#github?.status?.state === "connected") {
      if (request.action === "confirm") await this.githubConnection({ action: "refresh" });
      else await this.githubConnection({ action: "installations", page: 1 });
    }
  }

  clearRemoteInvite(): void {
    this.#remoteInvite = undefined;
    this.#remoteInviteError = undefined;
  }

  async loadAccounts(): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined || this.#accountsPending) return;
    const generation = this.#owner.generation();
    this.#accountsPending = true;
    this.#owner.publish();
    try {
      this.#accounts = await session.discoverAccounts();
      if (!this.#owner.current(generation)) return;
      this.#accountsError = undefined;
    } catch (error) {
      if (!this.#owner.current(generation)) return;
      this.#accountsError = this.#owner.errorCode(error);
    } finally {
      if (this.#owner.current(generation)) this.#accountsPending = false;
    }
    this.#owner.publish();
  }

  linkAccount(request: Parameters<BrowserSession["linkAccount"]>[0]): Promise<void> {
    return this.#changeAccount(request);
  }

  updateAccount(request: Parameters<BrowserSession["updateAccount"]>[0]): Promise<void> {
    return this.#changeAccount(request);
  }

  async loadRepositories(projectId: string): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined || this.#repositoryPending.has(projectId)) return;
    const generation = this.#owner.generation();
    this.#repositoryPending.add(projectId);
    this.#owner.publish();
    try {
      const repositories = await session.getRepositories(projectId);
      if (!this.#owner.current(generation)) return;
      this.#repositories.set(projectId, repositories);
      if (!this.#repositoryMutationErrors.has(projectId)) this.#repositoryErrors.delete(projectId);
    } catch (error) {
      if (!this.#owner.current(generation)) return;
      this.#repositoryErrors.set(projectId, this.#owner.errorCode(error));
    } finally {
      this.#repositoryPending.delete(projectId);
    }
    this.#owner.publish();
  }

  async mutateRepository(request: RepositoryMutation): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined || this.#repositoryPending.has(request.projectId)) return;
    const generation = this.#owner.generation();
    this.#repositoryPending.add(request.projectId);
    this.#owner.publish();
    try {
      await session.mutateRepository(request);
      if (!this.#owner.current(generation)) return;
      this.#repositoryMutationErrors.delete(request.projectId);
      this.#repositoryErrors.delete(request.projectId);
    } catch (error) {
      if (!this.#owner.current(generation)) return;
      this.#repositoryMutationErrors.add(request.projectId);
      this.#repositoryErrors.set(request.projectId, this.#owner.errorCode(error));
    } finally {
      this.#repositoryPending.delete(request.projectId);
    }
    this.#owner.publish();
    await this.loadRepositories(request.projectId);
  }

  async createProject(request: { name: string; root: string }): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined) return;
    const generation = this.#owner.generation();
    try {
      await session.createProject(request);
      if (!this.#owner.current(generation)) return;
      this.#repositoryErrors.delete("create");
    } catch (error) {
      if (!this.#owner.current(generation)) return;
      this.#repositoryErrors.set("create", this.#owner.errorCode(error));
    }
    this.#owner.publish();
  }

  async #changeAccount(request: Parameters<BrowserSession["linkAccount"]>[0] | Parameters<BrowserSession["updateAccount"]>[0]): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined || this.#accountsPending) return;
    const generation = this.#owner.generation();
    this.#accountsPending = true;
    this.#owner.publish();
    try {
      if ("accountId" in request) await session.updateAccount(request);
      else await session.linkAccount(request);
      if (!this.#owner.current(generation)) return;
      this.#accountsError = undefined;
    } catch (error) {
      if (!this.#owner.current(generation)) return;
      this.#accountsError = this.#owner.errorCode(error);
      this.#accountsPending = false;
      this.#owner.publish();
      return;
    } finally {
      if (this.#owner.current(generation)) this.#accountsPending = false;
    }
    this.#owner.publish();
    await this.loadAccounts();
  }

  async loadDevices(): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined || this.#devicesPending) return;
    const generation = this.#owner.generation();
    this.#devicesPending = true;
    try {
      this.#devices = await session.listBrowserClients();
      if (!this.#owner.current(generation)) return;
      this.#devicesError = undefined;
    } catch (error) {
      if (!this.#owner.current(generation)) return;
      this.#devicesError = this.#owner.errorCode(error);
    } finally {
      if (this.#owner.current(generation)) this.#devicesPending = false;
    }
    this.#owner.publish();
  }

  /** Withdraws one identity, then rereads the list so it is gone from it. */
  async revokeDevice(request: { clientId: string; expectedRevision: bigint }): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined || this.#devicesPending) return;
    const generation = this.#owner.generation();
    this.#devicesPending = true;
    try {
      await session.revokeBrowserClient(request);
      if (!this.#owner.current(generation)) return;
      this.#devicesError = undefined;
    } catch (error) {
      if (!this.#owner.current(generation)) return;
      this.#devicesError = this.#owner.errorCode(error);
      this.#devicesPending = false;
      this.#owner.publish();
      return;
    } finally {
      if (this.#owner.current(generation)) this.#devicesPending = false;
    }
    await this.loadDevices();
  }

  async inviteRemote(): Promise<void> {
    const session = this.#owner.session();
    if (!this.#owner.ready() || session === undefined || this.#remoteInvitePending) return;
    const generation = this.#owner.generation();
    this.#remoteInvitePending = true;
    try {
      const invite = await session.inviteRemote();
      if (!this.#owner.current(generation)) return;
      this.#remoteInvite = { link: invite.link, svg: invite.svg, expiresAtMs: invite.expiresAtMs };
      this.#remoteInviteError = undefined;
    } catch (error) {
      if (!this.#owner.current(generation)) return;
      this.#remoteInvite = undefined;
      this.#remoteInviteError = this.#owner.errorCode(error);
    } finally {
      this.#remoteInvitePending = false;
    }
    this.#owner.publish();
  }

  dismissRemoteInvite(): void {
    this.#remoteInvite = undefined;
    this.#remoteInviteError = undefined;
    this.#owner.publish();
  }
}
