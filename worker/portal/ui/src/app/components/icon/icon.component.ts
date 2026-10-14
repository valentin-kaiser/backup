import { ChangeDetectionStrategy, Component, input } from '@angular/core';

/**
 * A Material icon, name is its ligature, see https://fonts.google.com/icons. The font is bundled with the app,
 * angular.json adds its stylesheet.
 */
@Component({
  selector: 'app-icon',
  template: `<span class="material-icons-outlined" aria-hidden="true">{{ name() }}</span>`,
  styles: `
    :host {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 1.2em;
      height: 1.2em;
      flex: none;
    }
    span {
      font-size: 1.2em;
      line-height: 1;
      user-select: none;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class IconComponent {
  readonly name = input.required<string>();
}
