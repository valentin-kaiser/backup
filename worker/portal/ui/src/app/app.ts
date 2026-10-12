import { ChangeDetectionStrategy, Component, inject } from '@angular/core';
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';

import { ConfirmDialogComponent } from './components/confirm-dialog/confirm-dialog.component';
import { IconComponent, IconName } from './components/icon/icon.component';
import { ToastsComponent } from './components/toasts/toasts.component';
import { ControlService } from './global/services/control/control.service';

interface NavItem {
  path: string;
  label: string;
  icon: IconName;
  exact: boolean;
}

@Component({
  selector: 'app-root',
  imports: [
    RouterOutlet,
    RouterLink,
    RouterLinkActive,
    IconComponent,
    ToastsComponent,
    ConfirmDialogComponent,
  ],
  templateUrl: './app.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class App {
  protected readonly control = inject(ControlService);

  protected readonly nav: NavItem[] = [
    { path: '/', label: 'Overview', icon: 'dashboard', exact: true },
    { path: '/jobs', label: 'Jobs', icon: 'jobs', exact: false },
    { path: '/history', label: 'History', icon: 'history', exact: false },
    { path: '/snapshots', label: 'Snapshots', icon: 'snapshots', exact: false },
  ];

  constructor() {
    this.control.start();
  }
}
