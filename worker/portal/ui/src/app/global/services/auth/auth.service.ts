import { Injectable, effect, inject, signal, untracked } from '@angular/core';

import { Empty } from '../../model/google/protobuf/empty';
import { Session } from '../../model/backup/api/v1/auth';
import { ApiError, api } from '../../utils/api';
import { ControlService } from '../control/control.service';
import { RpcService } from '../rpc/rpc.service';

/**
 * Whether somebody is signed in. The portal has one password and no users: whoever knows it can do everything.
 * The session is an HttpOnly cookie the script cannot read, so the state comes from `Auth.GetSession`. The connection
 * to the daemon only exists while somebody is signed in.
 */
@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly rpc = inject(RpcService);
  private readonly control = inject(ControlService);

  readonly signedIn = signal(false);
  /** False until the first check finished, so the sign-in does not flash for a signed-in browser. */
  readonly ready = signal(false);

  constructor() {
    // A websocket that closes can mean the session ended, which the browser does not tell: ask.
    effect(() => {
      if (this.signedIn() && !this.rpc.connected()) {
        untracked(() => void this.verify());
      }
    });
  }

  /** Looks for an existing session, called once at start. */
  async load(): Promise<void> {
    try {
      await api('Auth', 'GetSession', {}, Session);
      this.apply(true);
    } catch {
      this.apply(false);
    }
    this.ready.set(true);
  }

  async login(password: string): Promise<void> {
    await api('Auth', 'Login', { password }, Session);
    this.apply(true);
  }

  async logout(): Promise<void> {
    try {
      await api('Auth', 'Logout', {}, Empty);
    } finally {
      this.apply(false);
    }
  }

  private async verify(): Promise<void> {
    try {
      await api('Auth', 'GetSession', {}, Session);
    } catch (error) {
      // Only a rejected session signs out, a network error keeps the page and the reconnect going.
      if (error instanceof ApiError && error.status === 401) {
        this.apply(false);
      }
    }
  }

  private apply(signedIn: boolean): void {
    const was = this.signedIn();
    this.signedIn.set(signedIn);
    if (signedIn && !was) {
      this.rpc.connect();
      this.control.start();
    } else if (!signedIn && was) {
      this.control.stop();
      this.rpc.disconnect();
      this.control.reset();
    }
  }
}
