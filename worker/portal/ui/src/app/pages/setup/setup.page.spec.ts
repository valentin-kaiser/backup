import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';

import { ControlService } from '../../global/services/control/control.service';
import { SetupPage } from './setup.page';

describe('SetupPage', () => {
  let control: any;

  beforeEach(() => {
    control = {
      defaults: signal({ passwordSecret: 'backup' }),
      setSecret: vi.fn(async (_n: string, _v: string, generate: boolean) => (generate ? 'GENERATED-VALUE' : '')),
    };
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: ControlService, useValue: control }] });
  });

  function create() {
    const fixture = TestBed.createComponent(SetupPage);
    fixture.detectChanges();
    return { fixture, page: fixture.componentInstance as any, el: fixture.nativeElement as HTMLElement };
  }

  it('shows a generated password once and waits for the admin to confirm it', async () => {
    const { fixture, page, el } = create();
    const done = vi.fn();
    page.done.subscribe(done);
    await page.store();
    fixture.detectChanges();
    expect(control.setSecret).toHaveBeenCalledWith('backup', '', true);
    expect(el.textContent).toContain('GENERATED-VALUE');
    expect(el.querySelector<HTMLButtonElement>('button.primary')!.disabled).toBe(true);
    page.acknowledged.set(true);
    fixture.detectChanges();
    el.querySelector<HTMLButtonElement>('button.primary')!.click();
    fixture.detectChanges();
    expect(el.textContent).not.toContain('GENERATED-VALUE');
    expect(done).toHaveBeenCalled();
  });

  it('stores an entered password and finishes', async () => {
    const { page } = create();
    const done = vi.fn();
    page.done.subscribe(done);
    page.generate.set(false);
    page.value.set('secret');
    page.again.set('other');
    expect(page.valid()).toBe(false);
    page.again.set('secret');
    await page.store();
    expect(control.setSecret).toHaveBeenCalledWith('backup', 'secret', false);
    expect(done).toHaveBeenCalled();
  });

  it('reports a refusal and stays on the password', async () => {
    control.setSecret.mockRejectedValueOnce(new Error('invalid value'));
    const { fixture, page, el } = create();
    await page.store();
    fixture.detectChanges();
    expect(el.textContent).toContain('invalid value');
    expect(page.step()).toBe('password');
  });

  it('tells the app when it is finished', async () => {
    const { page } = create();
    const done = vi.fn();
    page.done.subscribe(done);
    page.finish();
    expect(done).toHaveBeenCalled();
  });
});
