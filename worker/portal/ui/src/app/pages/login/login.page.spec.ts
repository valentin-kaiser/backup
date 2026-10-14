import { TestBed } from '@angular/core/testing';

import { AuthService } from '../../global/services/auth/auth.service';
import { LoginPage } from './login.page';

describe('LoginPage', () => {
  let auth: { login: ReturnType<typeof vi.fn> };

  beforeEach(() => {
    auth = { login: vi.fn(async () => undefined) };
    TestBed.configureTestingModule({ providers: [{ provide: AuthService, useValue: auth }] });
  });

  function create() {
    const fixture = TestBed.createComponent(LoginPage);
    fixture.detectChanges();
    return { fixture, page: fixture.componentInstance as any, el: fixture.nativeElement as HTMLElement };
  }

  it('asks for a password and nothing else', () => {
    const { el } = create();
    const visible = [...el.querySelectorAll<HTMLInputElement>('input')].filter((i) => !i.classList.contains('sr-only'));
    expect(visible.map((i) => i.type)).toEqual(['password']);
    expect(el.textContent).not.toMatch(/user/i);
  });

  it('signs in with the password', async () => {
    const { page } = create();
    page.password.set('the password');
    await page.submit();
    expect(auth.login).toHaveBeenCalledWith('the password');
  });

  it('does not submit an empty password', async () => {
    const { page } = create();
    await page.submit();
    expect(auth.login).not.toHaveBeenCalled();
  });

  it('shows why it failed and clears the field', async () => {
    auth.login.mockRejectedValueOnce(new Error('wrong password'));
    const { fixture, page, el } = create();
    page.password.set('nope');
    await page.submit();
    fixture.detectChanges();
    expect(el.textContent).toContain('wrong password');
    expect(page.password()).toBe('');
  });
});
