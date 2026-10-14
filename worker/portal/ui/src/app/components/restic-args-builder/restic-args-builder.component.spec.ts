import { TestBed } from '@angular/core/testing';

import { JobType } from '../../global/utils/restic-args';
import { ResticArgsBuilderComponent } from './restic-args-builder.component';

describe('ResticArgsBuilderComponent', () => {
  function create(type: JobType, args: string) {
    const fixture = TestBed.createComponent(ResticArgsBuilderComponent);
    fixture.componentRef.setInput('type', type);
    fixture.componentRef.setInput('args', args);
    fixture.detectChanges();
    const element = fixture.nativeElement as HTMLElement;
    return {
      fixture,
      component: fixture.componentInstance as any,
      element,
      input: (id: string) => element.querySelector(`#arg-${id}`) as HTMLInputElement,
      type: (target: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement, text: string, event = 'input') => {
        target.value = text;
        target.dispatchEvent(new Event(event));
        fixture.detectChanges();
      },
      preview: () => element.querySelector('.preview code')?.textContent,
    };
  }

  it('shows the options of the job type with the current values', () => {
    const page = create('backup', '--exclude-caches --exclude-if-present=.nobackup --exclude "*.tmp"');
    expect((page.input('exclude-if-present') as HTMLInputElement).value).toBe('.nobackup');
    expect(page.element.querySelectorAll('.items .tag')).toHaveLength(1);
    expect(page.element.querySelectorAll('input[type="checkbox"]:checked')).toHaveLength(1);
    expect(page.element.textContent).toContain('Exclusions');
    expect(page.element.textContent).not.toContain('Keep');
  });

  it('does not rewrite the arguments just by opening them', () => {
    const page = create('backup', '--exclude-if-present=.nobackup --exclude-caches');
    expect(page.component.args()).toBe('--exclude-if-present=.nobackup --exclude-caches');
  });

  it('writes the arguments when an option changes', () => {
    const page = create('forget', '--prune --keep-last 7');
    page.type(page.input('keep-daily'), '14');
    expect(page.component.args()).toBe('--keep-last 7 --keep-daily 14 --prune');

    page.type(page.input('keep-last'), '');
    expect(page.component.args()).toBe('--keep-daily 14 --prune');
  });

  it('toggles a switch and picks a choice', () => {
    const page = create('forget', '');
    const prune = page.element.querySelector('input[type="checkbox"]') as HTMLInputElement;
    prune.checked = true;
    prune.dispatchEvent(new Event('change'));
    page.fixture.detectChanges();
    expect(page.component.args()).toBe('--prune');

    page.type(page.input('group-by'), 'host,tags', 'change');
    expect(page.component.args()).toBe('--group-by host,tags --prune');
  });

  it('adds and removes entries of a list', () => {
    const page = create('backup', '');
    const input = page.input('exclude');
    page.type(input, '*.tmp');
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
    page.fixture.detectChanges();
    page.type(page.input('exclude'), 'node_modules');
    (page.element.querySelectorAll('.add button')[0] as HTMLElement).click();
    page.fixture.detectChanges();
    expect(page.component.args()).toBe('--exclude "*.tmp" --exclude node_modules');

    (page.element.querySelector('.items .x') as HTMLElement).click();
    page.fixture.detectChanges();
    expect(page.component.args()).toBe('--exclude node_modules');
  });

  it('keeps arguments it does not know', () => {
    const page = create('backup', '--limit-upload 500 --exclude-caches');
    expect((page.element.querySelector('#arg-extra') as HTMLTextAreaElement).value).toBe('--limit-upload 500');
    page.type(page.input('host'), 'nas');
    expect(page.component.args()).toBe('--exclude-caches --host nas --limit-upload 500');

    page.type(page.element.querySelector('#arg-extra') as HTMLTextAreaElement, '--verbose');
    expect(page.component.args()).toBe('--exclude-caches --host nas --verbose');
  });

  it('shows the command that will run', () => {
    const page = create('check', '--read-data-subset=5%');
    expect(page.preview()).toBe('restic check --read-data-subset=5%');
  });

  it('warns about quotes that are not closed and loses nothing', () => {
    const page = create('backup', '--exclude "open');
    expect(page.element.querySelector('.field-error')).not.toBeNull();
    expect((page.element.querySelector('#arg-extra') as HTMLTextAreaElement).value).toBe('--exclude "open');
  });

  it('follows arguments that change from the outside, also when the type changes', () => {
    const page = create('backup', '--exclude-caches');
    page.fixture.componentRef.setInput('args', '--host nas');
    page.fixture.detectChanges();
    expect((page.input('host') as HTMLInputElement).value).toBe('nas');

    // The same text, read for another type: --prune is not a check option.
    page.fixture.componentRef.setInput('args', '--prune --read-data');
    page.fixture.componentRef.setInput('type', 'check');
    page.fixture.detectChanges();
    expect(page.element.querySelectorAll('input[type="checkbox"]:checked')).toHaveLength(1);
    expect((page.element.querySelector('#arg-extra') as HTMLTextAreaElement).value).toBe('--prune');
  });
});
