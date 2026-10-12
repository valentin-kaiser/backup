import { Routes } from '@angular/router';

import { BlankPage } from './pages/blank/blank.page';

export const routes: Routes = [
  // The overview is always on screen, every other route opens in the drawer next to it.
  { path: '', pathMatch: 'full', title: 'Overview', component: BlankPage },
  {
    path: 'jobs/new',
    title: 'New job',
    loadComponent: () => import('./pages/job-edit/job-edit.page').then((m) => m.JobEditPage),
  },
  {
    path: 'jobs/:name',
    title: 'Edit job',
    loadComponent: () => import('./pages/job-edit/job-edit.page').then((m) => m.JobEditPage),
  },
  {
    path: 'runs/:id',
    title: 'Run',
    loadComponent: () => import('./pages/run/run.page').then((m) => m.RunPage),
  },
  {
    path: 'snapshots/:snapshot',
    title: 'Snapshot content',
    loadComponent: () => import('./pages/browse/browse.page').then((m) => m.BrowsePage),
  },
  {
    path: 'audit',
    title: 'Audit log',
    loadComponent: () => import('./pages/audit/audit.page').then((m) => m.AuditPage),
  },
  {
    path: 'secrets',
    title: 'Secrets',
    loadComponent: () => import('./pages/secrets/secrets.page').then((m) => m.SecretsPage),
  },
  { path: '**', redirectTo: '' },
];
