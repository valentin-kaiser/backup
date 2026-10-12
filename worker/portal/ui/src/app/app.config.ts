import {
  ApplicationConfig,
  inject,
  provideAppInitializer,
  provideBrowserGlobalErrorListeners,
} from '@angular/core';
import { provideRouter, withComponentInputBinding, withHashLocation } from '@angular/router';

import { routes } from './app.routes';
import { RpcService } from './global/services/rpc/rpc.service';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    // Hash routes, so the Go file server needs no fallback to index.html. Route parameters and
    // query parameters are bound to the inputs of the pages.
    provideRouter(routes, withHashLocation(), withComponentInputBinding()),
    // There is no sign-in, the portal talks to the daemon as soon as it is open.
    provideAppInitializer(() => inject(RpcService).connect()),
  ],
};
