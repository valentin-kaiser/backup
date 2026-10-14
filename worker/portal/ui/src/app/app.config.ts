import {
  ApplicationConfig,
  inject,
  provideAppInitializer,
  provideBrowserGlobalErrorListeners,
} from '@angular/core';
import { provideRouter, withComponentInputBinding, withHashLocation, withInMemoryScrolling } from '@angular/router';

import { routes } from './app.routes';
import { AuthService } from './global/services/auth/auth.service';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    // Hash routes, so the Go file server needs no fallback to index.html. Route parameters and
    // query parameters are bound to the inputs of the pages.
    provideRouter(routes, withHashLocation(), withComponentInputBinding(), withInMemoryScrolling({ anchorScrolling: 'enabled' })),
    // Look for a session first, the connection to the daemon opens once somebody is signed in.
    provideAppInitializer(() => inject(AuthService).load()),
  ],
};
