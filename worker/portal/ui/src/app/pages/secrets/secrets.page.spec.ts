import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';

import { SecretInfo } from '../../global/model/backup/api/v1/control';
import { ConfirmService } from '../../global/services/confirm/confirm.service';
import { ControlService } from '../../global/services/control/control.service';
import { SecretsPage } from './secrets.page';

describe('SecretsPage', () => {
  let control: any;
  let confirmed: boolean;

  beforeEach(() => {
    confirmed = true;
    control = {
      error: signal(''),
      secrets: signal([
        SecretInfo.create({ name: 'photos', repositories: ['main', 'usb'] }),
        SecretInfo.create({ name: 'spare' }),
      ]),
      loadSecrets: vi.fn(async () => undefined),
      setSecret: vi.fn(async (_n: string, _v: string, generate: boolean) => (generate ? 'GENERATED-VALUE' : '')),
      changeRepositoryPassword: vi.fn(async () => ({ repositories: 1, warnings: ['old key stays'], generatedValue: '' })),
      deleteSecret: vi.fn(async () => undefined),
    };
    TestBed.configureTestingModule({
      providers: [
        provideRouter([]),
        { provide: ControlService, useValue: control },
        { provide: ConfirmService, useValue: { ask: vi.fn(async () => confirmed) } },
      ],
    });
  });

  function create() {
    const fixture = TestBed.createComponent(SecretsPage);
    fixture.detectChanges();
    return { fixture, page: fixture.componentInstance as any, el: fixture.nativeElement as HTMLElement };
  }

  it('lists the secrets with the repositories that use them and never a value', () => {
    const { el } = create();
    expect(control.loadSecrets).toHaveBeenCalled();
    const rows = [...el.querySelectorAll('tbody tr')].map((r) => r.textContent!.replace(/\s+/g, ' ').trim());
    expect(rows[0]).toContain('photos');
    expect(rows[0]).toContain('repository main, repository usb');
    expect(rows[1]).toContain('nothing yet');
  });

  it('only allows deleting and changing what fits: a used secret cannot be deleted, an unused one has no repository', () => {
    const { el } = create();
    const rows = el.querySelectorAll('tbody tr');
    expect((rows[0].querySelector('button.icon') as HTMLButtonElement).disabled).toBe(true);
    expect((rows[1].querySelector('button.icon') as HTMLButtonElement).disabled).toBe(false);
    const change = [...rows[1].querySelectorAll('button')].find((b) => b.textContent!.includes('Change password'))!;
    expect(change.disabled).toBe(true);
  });

  it('shows a generated password once and forgets it when dismissed', async () => {
    const { page, fixture, el } = create();
    page.newName.set('fresh');
    await page.create();
    fixture.detectChanges();
    expect(control.setSecret).toHaveBeenCalledWith('fresh', '', true);
    expect(el.querySelector('.secret-value')!.textContent).toContain('GENERATED-VALUE');
    page.generated.set(null);
    fixture.detectChanges();
    expect(el.textContent).not.toContain('GENERATED-VALUE');
  });

  it('stores a typed password without showing anything', async () => {
    const { page, fixture, el } = create();
    page.newName.set('docs');
    page.newGenerate.set(false);
    page.newValue.set('typed password');
    await page.create();
    fixture.detectChanges();
    expect(control.setSecret).toHaveBeenCalledWith('docs', 'typed password', false);
    expect(el.querySelector('.secret-value')).toBeNull();
    expect(page.newValue()).toBe('');
  });

  it('asks before changing a repository password and reports warnings', async () => {
    const { page, fixture, el } = create();
    page.start(control.secrets()[0], 'change');
    page.value.set('brand new');
    page.again.set('brand new');
    await page.submit();
    fixture.detectChanges();
    expect(control.changeRepositoryPassword).toHaveBeenCalledWith('photos', 'brand new', false);
    expect(el.textContent).toContain('old key stays');
    expect(page.action()).toBeNull();
  });

  it('does nothing when the change is not confirmed', async () => {
    confirmed = false;
    const { page } = create();
    page.start(control.secrets()[0], 'change');
    page.value.set('brand new');
    page.again.set('brand new');
    await page.submit();
    expect(control.changeRepositoryPassword).not.toHaveBeenCalled();
    expect(page.action()).not.toBeNull();
  });

  it('refuses a mismatching repeat and shows errors of the daemon', async () => {
    const { page } = create();
    page.start(control.secrets()[1], 'set');
    page.value.set('one');
    page.again.set('two');
    expect(page.actionValid()).toBe(false);
    page.again.set('one');
    control.setSecret.mockRejectedValueOnce(new Error('the secret must not contain line breaks'));
    await page.submit();
    expect(page.error()).toContain('line breaks');
  });
});
