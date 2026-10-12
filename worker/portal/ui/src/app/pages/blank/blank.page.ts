import { ChangeDetectionStrategy, Component } from '@angular/core';

/** The route of the overview: nothing to show in the drawer. */
@Component({
  selector: 'app-blank',
  template: '',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class BlankPage {}
