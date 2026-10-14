import { TestBed } from '@angular/core/testing';

import { CronBuilderComponent } from './cron-builder.component';

describe('CronBuilderComponent', () => {
  function create(value = '0 2 * * *') {
    const fixture = TestBed.createComponent(CronBuilderComponent);
    fixture.componentRef.setInput('value', value);
    fixture.detectChanges();
    const element = fixture.nativeElement as HTMLElement;
    return {
      fixture,
      component: fixture.componentInstance as any,
      element,
      field: () => element.querySelector('input.mono') as HTMLInputElement,
      click: (selector: string, index = 0) => {
        (element.querySelectorAll(selector)[index] as HTMLElement).click();
        fixture.detectChanges();
      },
      type: (input: HTMLInputElement, text: string) => {
        input.value = text;
        input.dispatchEvent(new Event('input'));
        fixture.detectChanges();
      },
    };
  }

  it('describes the schedule and lists the next runs', () => {
    const { element, component } = create('0 2 * * *');
    expect(element.querySelector('.summary')?.textContent).toContain('Every day at 02:00');
    expect(element.querySelectorAll('.next li')).toHaveLength(5);
    expect(component.value()).toBe('0 2 * * *');
  });

  it('shows what is wrong instead of a description', () => {
    const { element, type, field } = create();
    type(field(), '61 * * * *');
    expect(element.querySelector('.field-error')?.textContent).toContain('minute');
    expect(element.querySelector('.summary')).toBeNull();
    expect(element.querySelector('.next')).toBeNull();
  });

  it('is quiet about an empty field unless the form says it is required', () => {
    const { element, fixture, type, field } = create();
    type(field(), '');
    expect(element.querySelector('.field-error')).toBeNull();
    fixture.componentRef.setInput('error', 'A schedule is required');
    fixture.detectChanges();
    expect(element.querySelector('.field-error')?.textContent).toContain('required');
  });

  it('picks a preset', () => {
    const { component, click } = create('0 2 * * *');
    click('button[aria-expanded]');
    click('.preset', 1); // every 15 minutes
    expect(component.value()).toBe('*/15 * * * *');
  });

  it('opens on the preset that matches and on custom otherwise', () => {
    const preset = create('*/15 * * * *');
    preset.click('button[aria-expanded]');
    expect(preset.element.querySelector('.preset.active code')?.textContent).toBe('*/15 * * * *');

    const custom = create('7 3 * * *');
    custom.click('button[aria-expanded]');
    expect(custom.element.querySelectorAll('.field-card')).toHaveLength(5);
  });

  describe('the custom builder', () => {
    function custom(value: string) {
      const page = create(value);
      page.click('button[aria-expanded]');
      return page;
    }
    const card = (page: ReturnType<typeof create>, index: number) => page.element.querySelectorAll('.field-card')[index] as HTMLElement;
    const mode = (page: ReturnType<typeof create>, card: number, label: string) => {
      const buttons = Array.from(page.element.querySelectorAll('.field-card')[card].querySelectorAll('.seg button')) as HTMLElement[];
      buttons.find((b) => b.textContent?.trim() === label)?.click();
      page.fixture.detectChanges();
    };

    it('shows the controls of each field for the expression', () => {
      const page = custom('*/15 8-17 1,15 * MON-FRI');
      const modes = Array.from(page.element.querySelectorAll('.field-card')).map(
        (c) => c.querySelector('.seg button[aria-checked="true"]')?.textContent?.trim(),
      );
      expect(modes).toEqual(['Interval', 'Range', 'List', 'Every', 'Range']);
    });

    it('writes the expression when a mode or a value changes', () => {
      const page = custom('* * * * *');
      mode(page, 0, 'At');
      expect(page.component.value()).toBe('0 * * * *');

      const minute = card(page, 0).querySelector('input[type="number"]') as HTMLInputElement;
      page.type(minute, '30');
      expect(page.component.value()).toBe('30 * * * *');

      mode(page, 1, 'At');
      const hour = card(page, 1).querySelector('select') as HTMLSelectElement;
      hour.value = '3';
      hour.dispatchEvent(new Event('change'));
      expect(page.component.value()).toBe('30 3 * * *');
    });

    it('picks weekdays by name', () => {
      const page = custom('0 3 * * *');
      mode(page, 4, 'List');
      const buttons = Array.from(card(page, 4).querySelectorAll('.toggle')) as HTMLElement[];
      expect(buttons.map((b) => b.textContent?.trim())).toEqual(['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']);
      buttons[1].click();
      buttons[3].click();
      page.fixture.detectChanges();
      expect(page.component.value()).toBe('0 3 * * 0,1,3');
    });

    it('builds an interval and an inclusive range', () => {
      const page = custom('* * * * *');
      mode(page, 0, 'Interval');
      const [step] = Array.from(card(page, 0).querySelectorAll('input[type="number"]')) as HTMLInputElement[];
      page.type(step, '10');
      expect(page.component.value()).toBe('*/10 * * * *');

      mode(page, 1, 'Range');
      expect(page.component.value()).toBe('*/10 0-23 * * *');
    });

    it('adds and removes values of a list', () => {
      const page = custom('0 3 * * *');
      mode(page, 0, 'List');
      const input = card(page, 0).querySelector('input[type="number"]') as HTMLInputElement;
      page.type(input, '30');
      (card(page, 0).querySelector('.pair button') as HTMLElement).click();
      page.fixture.detectChanges();
      expect(page.component.value()).toBe('0,30 3 * * *');

      (card(page, 0).querySelector('.chips .x') as HTMLElement).click();
      page.fixture.detectChanges();
      expect(page.component.value()).toBe('30 3 * * *');
    });

    it('follows what is typed in the field', () => {
      const page = custom('0 3 * * *');
      page.type(page.field(), '*/5 * * * *');
      expect(card(page, 0).querySelector('.seg button[aria-checked="true"]')?.textContent?.trim()).toBe('Interval');
    });

    it('keeps its controls while the expression is incomplete', () => {
      const page = custom('0 3 * * *');
      mode(page, 0, 'Range'); // 0-59
      const [from] = Array.from(card(page, 0).querySelectorAll('input[type="number"]')) as HTMLInputElement[];
      page.type(from, '50');
      page.type(Array.from(card(page, 0).querySelectorAll('input[type="number"]'))[1] as HTMLInputElement, '10');
      // 50-10 is not valid, but the controls still show a range and the field says why.
      expect(page.component.value()).toBe('50-10 3 * * *');
      expect(card(page, 0).querySelector('.seg button[aria-checked="true"]')?.textContent?.trim()).toBe('Range');
      expect(page.element.querySelector('.field-error')).not.toBeNull();
    });
  });
});
