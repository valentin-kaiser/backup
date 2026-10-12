import { Routes } from '@angular/router';

export const routes: Routes = [
  {
    path: '',
    pathMatch: 'full',
    title: 'Overview',
    loadComponent: () => import('./pages/dashboard/dashboard.page').then((m) => m.DashboardPage),
  },
  {
    path: 'jobs',
    pathMatch: 'full',
    title: 'Jobs',
    loadComponent: () => import('./pages/jobs/jobs.page').then((m) => m.JobsPage),
  },
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
    path: 'history',
    title: 'History',
    loadComponent: () => import('./pages/history/history.page').then((m) => m.HistoryPage),
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
    path: 'snapshots',
    title: 'Snapshots',
    loadComponent: () => import('./pages/snapshots/snapshots.page').then((m) => m.SnapshotsPage),
  },
  { path: '**', redirectTo: '' },
];
