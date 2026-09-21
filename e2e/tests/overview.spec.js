// @ts-check
/*
 * The overview's two pictures: the run-up from today to the day, and the money
 * bar. Both are drawings of facts the page also states in words, so what is
 * checked here is that the drawing agrees with the facts - a mark in the wrong
 * place is worse than no mark.
 *
 * The server under test is configured for 12 June 2030 at +09:00.
 */
const { test, expect } = require('@playwright/test');
const { STORAGE_KEY, addBudgetLine, addTask, gotoTab } = require('./helpers');

/** The left offset of each mark on the scale, as a percentage, and the name beside it. */
const marks = (page) => page.locator('#runupMarks .runup-mark').evaluateAll(
  (els) => els.map((el) => {
    const name = el.querySelector('.runup-name');
    return { at: parseFloat(el.style.left), late: el.classList.contains('late'), label: name ? name.textContent : '' };
  }),
);

/**
 * Where everything on the scale actually is, in page pixels: each mark, and
 * its name and leader if it has them. `clipped` is a name the stylesheet cut
 * off, as opposed to one the page shortened at a word.
 */
const geometry = (page) => page.locator('#runupMarks .runup-mark').evaluateAll((els) => els.map((el) => {
  const box = (node) => {
    if (!node) return null;
    const r = node.getBoundingClientRect();
    return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
  };
  const label = el.querySelector('.runup-label');
  const name = el.querySelector('.runup-name') || label;
  const count = el.querySelector('.runup-count');
  return {
    mark: box(el),
    label: box(label),
    leader: box(el.querySelector('.runup-leader')),
    name: name ? name.textContent : null,
    clipped: !!label && (label.scrollWidth > label.clientWidth + 1 || (name !== label && name.clientWidth > 0 && name.scrollWidth > name.clientWidth + 1)),
    count: count ? Number(count.textContent) : 1,
    late: el.classList.contains('late'),
  };
}));

const overlap = (a, b) => !!a && !!b && a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;

/**
 * Which mark a press at the very middle of the overdue ring lands on, named
 * as the page names it. A click says the same thing but takes the
 * actionability timeout to say it.
 */
const middleOfTheRing = (page) => page.locator('#runupMarks .runup-mark.late').evaluate((ring) => {
  const r = ring.getBoundingClientRect();
  const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
  const mark = hit && hit.closest('.runup-mark');
  return mark ? mark.getAttribute('aria-label') : null;
});

/**
 * What is in front at the middle of a counted mark's number, named as the page
 * names it, or by its class where it is not a mark. The day marker takes no
 * presses, so it is lent them for the length of the question: hit testing
 * follows paint order, and nothing else will say which of two things that
 * stand on the same spot is the one in view.
 */
const overTheCount = (page) => page.locator('#runupMarks .runup-mark.many .runup-count').evaluate((count) => {
  const day = document.querySelector('.runup-day');
  day.style.pointerEvents = 'auto';
  const r = count.getBoundingClientRect();
  const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
  day.style.pointerEvents = '';
  const mark = hit && hit.closest('.runup-mark');
  return mark ? mark.getAttribute('aria-label') : (hit ? hit.className : null);
});

/** Opens the planner with a planted set of tasks, at a given instant. */
async function openWith(page, when, tasks) {
  await page.clock.setFixedTime(new Date(when));
  await page.addInitScript(([key, payload]) => {
    try {
      if (!localStorage.getItem(key)) localStorage.setItem(key, payload);
    } catch (e) {
      /* not on the origin yet */
    }
  }, [STORAGE_KEY, JSON.stringify({
    ceiling: 0, inflationPct: 0, fxRate: 0, splitEvenly: false, sponsors: [], budgetItems: [], notes: [],
    tasks: tasks.map(([name, due, owner], i) => ({ id: 't' + i, name, owner: owner || '', due, status: 'not-started' })),
  })]);
  await page.goto('/');
  await expect(page.locator('body')).not.toHaveClass(/is-empty/);
}

/*
 * The shape real plans have, and the one a linear scale is worst at: 155 days
 * to go, six tasks in the first fifth of them - two only three days apart,
 * two only two - then nothing for three months, then two on consecutive days.
 * Three of the names are long enough that the scale used to cut them.
 */
const CROWDED_NOW = '2030-01-08T03:00:00Z';
const CROWDED = [
  ['Choose the menu and confirm the allergies', '2030-01-11', 'Ada'],
  ['Confirm the band', '2030-01-14', 'Grace'],
  ['Send the save-the-date cards to everyone', '2030-01-28', 'Ada'],
  ['Order the cake', '2030-02-02', 'Grace'],
  ['Book the photographer for the whole evening', '2030-02-04', 'Ada'],
  ['Pay the florist', '2030-02-08', 'Grace'],
  ['Print the seating plan and the place cards', '2030-05-04', 'Ada'],
  ['Collect the suits', '2030-05-05', 'Grace'],
];

test('open tasks are pinned on the run-up where their dates fall, and late ones are counted', async ({ page }) => {
  // Noon on 3 June where the event is: nine days to go.
  await page.clock.setFixedTime(new Date('2030-06-03T03:00:00Z'));
  await page.goto('/');
  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Order the cake', due: '2030-06-06' });          // 3 of 9 days along
  await addTask(page, { name: 'Confirm the band', due: '2030-06-09' });        // 6 of 9
  await addTask(page, { name: 'Pay the florist', due: '2030-05-20' });         // late
  await addTask(page, { name: 'Sign the contract', due: '2030-06-05', status: 'done' });
  await addTask(page, { name: 'Think about speeches' });                       // no date
  await gotoTab(page, 'overview');

  await expect(page.locator('#daysNum')).toHaveText('9');
  const pinned = await marks(page);
  // Done and undated tasks are not on it. The late one is, at today.
  expect(pinned.map((m) => Math.round(m.at))).toEqual([0, 33, 67]);
  expect(pinned.map((m) => m.late)).toEqual([true, false, false]);
  expect(pinned[1].label).toBe('Order the cake');
  await expect(page.locator('#runupFrom')).toHaveText('today – 1 overdue');
  // The word is carried in a visually-hidden span beside the date, because
  // the alarm colour says nothing to a screen reader and does not survive
  // forced colours.
  await expect(page.locator('#upNextList .due.late')).toHaveText(['2030-05-20 late']);

  // Finishing the late one takes it off the scale and out of the count.
  await gotoTab(page, 'tasks');
  // The third row added; an input's typed value is not an attribute to select on.
  await page.locator('#tasksBody tr').nth(2).locator('select.status-select').selectOption('done');
  await gotoTab(page, 'overview');
  expect((await marks(page)).map((m) => Math.round(m.at))).toEqual([33, 67]);
  await expect(page.locator('#runupFrom')).toHaveText('today');
});

test('with no distance left to draw there is no scale, and the date still shows', async ({ page }) => {
  // The day itself where the event is.
  await page.clock.setFixedTime(new Date('2030-06-12T03:00:00Z'));
  await page.goto('/');
  await expect(page.locator('#runup')).toHaveClass(/no-scale/);
  await expect(page.locator('#runupScale')).toBeHidden();
  await expect(page.locator('#daysNum')).toHaveText('0');
  await expect(page.locator('#statDaysLabel')).toHaveText('June 12, 2030');
});

test('names on the run-up never overlap, on a phone either', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.clock.setFixedTime(new Date('2030-01-04T03:00:00Z'));
  await page.goto('/');
  await gotoTab(page, 'tasks');
  for (const [name, due] of [['Choose the menu', '2030-02-12'], ['Send invitations', '2030-03-30'],
    ['Confirm the band', '2030-05-02'], ['Order the cake', '2030-05-20']]) {
    await addTask(page, { name, due });
  }
  await gotoTab(page, 'overview');
  await expect(page.locator('#runupMarks .runup-mark')).toHaveCount(4);
  // Names that would have run into each other are in lanes now, one above the
  // other, so "overlap" is a question about rectangles rather than about left
  // and right edges: two names may share a stretch of the scale, not a place.
  const boxes = (await geometry(page)).map((m) => m.label).filter(Boolean);
  expect(boxes.length).toBeGreaterThan(0);
  for (let i = 0; i < boxes.length; i++) {
    for (let j = i + 1; j < boxes.length; j++) {
      expect(overlap(boxes[i], boxes[j]), `names ${i} and ${j} are in the same place`).toBe(false);
    }
  }
  const scale = await page.locator('#runupScale').evaluate((el) => {
    const r = el.getBoundingClientRect();
    return { left: r.left, right: r.right };
  });
  for (const b of boxes) {
    expect(b.right, 'a name stays inside the scale').toBeLessThanOrEqual(scale.right + 8);
    expect(b.left, 'a name stays inside the scale').toBeGreaterThanOrEqual(scale.left - 8);
  }
});

for (const [width, height] of [[1280, 900], [390, 844]]) {
  test.describe(`a crowded month on the run-up, at ${width}px`, () => {
    test.beforeEach(async ({ page }) => {
      await page.setViewportSize({ width, height });
      await openWith(page, CROWDED_NOW, CROWDED);
      await expect(page.locator('#daysNum')).toHaveText('155');
    });

    test('no two names share a place, and none lies across another mark\'s leader', async ({ page }) => {
      const drawn = await geometry(page);
      const named = drawn.filter((m) => m.label);
      // The near term is the part people came to see, so it is named, not
      // dotted. A desktop has seven marks - the six, and the pair in May as
      // one - and a phone, at two pixels to the day, has three: the first two
      // tasks, the next four, and the pair. Every one of them carries a name.
      expect(drawn.length).toBe(width >= 1280 ? 7 : 3);
      expect(named.length).toBe(drawn.length);
      expect(named[0].name.startsWith('Choose the menu')).toBe(true);
      for (let i = 0; i < named.length; i++) {
        for (let j = 0; j < named.length; j++) {
          if (i === j) continue;
          if (i < j) expect(overlap(named[i].label, named[j].label), `"${named[i].name}" and "${named[j].name}" overlap`).toBe(false);
          expect(overlap(named[i].label, named[j].leader), `"${named[i].name}" lies across the leader of "${named[j].name}"`).toBe(false);
        }
      }
    });

    test('no two marks touch: they are apart, or they are one mark with a count', async ({ page }) => {
      const drawn = (await geometry(page)).sort((a, b) => a.mark.left - b.mark.left);
      for (let i = 1; i < drawn.length; i++) {
        expect(drawn[i].mark.left - drawn[i - 1].mark.right, `paper between marks ${i - 1} and ${i}`).toBeGreaterThanOrEqual(2);
      }
      // Merging hides nothing: the counts add up to the tasks there are.
      expect(drawn.reduce((n, m) => n + m.count, 0)).toBe(CROWDED.length);
      // The two on consecutive days in May are one mark that says "2".
      expect(drawn[drawn.length - 1].count).toBe(2);
    });

    test('a name is never cut through a word', async ({ page }) => {
      const full = CROWDED.map(([name]) => name);
      for (const m of (await geometry(page)).filter((g) => g.label)) {
        expect(m.clipped, `"${m.name}" is cut off by the stylesheet`).toBe(false);
        if (full.includes(m.name)) continue;
        // Shortened, then: a whole number of words of a real name, and the mark.
        expect(m.name.endsWith('\u2026'), `"${m.name}" is neither a task nor a shortened one`).toBe(true);
        const kept = m.name.slice(0, -1);
        expect(full.some((name) => name.startsWith(kept + ' ')), `"${m.name}" stops inside a word`).toBe(true);
      }
      // With a desktop's worth of room nothing needs shortening at all.
      if (width >= 1280) {
        const names = (await geometry(page)).map((g) => g.name).filter(Boolean);
        expect(names.every((name) => full.includes(name)), names.join(' | ')).toBe(true);
      }
    });

    test('every task can be reached from the scale', async ({ page }) => {
      const found = new Set();
      const all = page.locator('#runupMarks .runup-mark');
      const n = await all.count();
      for (let i = 0; i < n; i++) {
        await all.nth(i).click();
        const pop = page.locator('.runup-pop');
        await expect(pop).toBeVisible();
        for (const name of await pop.locator('li .what').allTextContents()) found.add(name);
        await page.keyboard.press('Escape');
        await expect(pop).toHaveCount(0);
      }
      expect([...found].sort()).toEqual(CROWDED.map(([name]) => name).sort());
    });
  });
}

test('the scale is one stop for the keyboard, and a mark opens and closes like the other popovers', async ({ page }) => {
  await openWith(page, CROWDED_NOW, CROWDED);
  const bar = page.locator('#runupMarks');
  await expect(bar).toHaveAttribute('role', 'toolbar');
  await expect(bar).toHaveAttribute('aria-label', 'What is due, from today to the day');
  await expect(page.locator('#runupScale')).not.toHaveAttribute('aria-hidden', 'true');
  // Seven marks, one of them in the tab order.
  await expect(bar.locator('.runup-mark')).toHaveCount(7);
  await expect(bar.locator('.runup-mark[tabindex="0"]')).toHaveCount(1);
  for (const label of await bar.locator('.runup-mark').evaluateAll((els) => els.map((el) => el.getAttribute('aria-label')))) {
    expect(label).toBeTruthy();
  }

  const first = bar.locator('.runup-mark').first();
  await first.focus();
  await page.keyboard.press('End');
  const last = bar.locator('.runup-mark').last();
  await expect(last).toBeFocused();
  await expect(last).toHaveAttribute('aria-label', '2 tasks, May 4 to May 5');
  await page.keyboard.press('ArrowLeft');
  await expect(bar.locator('.runup-mark').nth(5)).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(last).toBeFocused();
  // Wherever the arrows leave it is where Tab comes back to.
  await expect(bar.locator('.runup-mark[tabindex="0"]')).toHaveCount(1);
  await expect(last).toHaveAttribute('tabindex', '0');

  await page.keyboard.press('Enter');
  const pop = page.locator('.runup-pop');
  await expect(pop).toBeFocused();
  await expect(last).toHaveAttribute('aria-expanded', 'true');
  await expect(pop.locator('li')).toHaveText([/May 4.*Print the seating plan and the place cards.*Ada/, /May 5.*Collect the suits.*Grace/]);
  await page.keyboard.press('Escape');
  await expect(pop).toHaveCount(0);
  await expect(last).toBeFocused();
  await expect(last).toHaveAttribute('aria-expanded', 'false');

  // Pressing a mark that is open closes it rather than opening it again.
  await last.click();
  await expect(pop).toBeVisible();
  await last.click();
  await expect(pop).toHaveCount(0);
});

test('everything late is one mark at today, and it says what is late', async ({ page }) => {
  await openWith(page, CROWDED_NOW, [
    ['Pay the florist', '2029-12-20', 'Grace'],
    ['Sign the venue contract', '2030-01-02', 'Ada'],
    ['Order the cake', '2030-03-01', 'Grace'],
  ]);
  await expect(page.locator('#runupFrom')).toHaveText('today \u2013 2 overdue');
  const pinned = await marks(page);
  expect(pinned.map((m) => m.late)).toEqual([true, false]);
  expect(pinned[0].at).toBe(0);

  const late = page.locator('#runupMarks .runup-mark.late');
  await expect(late).toHaveAttribute('aria-label', '2 overdue');
  await late.click();
  await expect(page.locator('.runup-pop li .what')).toHaveText(['Pay the florist', 'Sign the venue contract']);
  await expect(page.locator('.runup-pop li .when.late')).toHaveCount(2);
});

test('the overdue ring is still the thing pressed when work is due today as well', async ({ page }) => {
  await openWith(page, CROWDED_NOW, [
    ['Pay the florist', '2029-12-20', 'Grace'],
    ['Confirm the band', '2030-01-08', 'Ada'],
    ['Collect the suits', '2030-01-08', 'Grace'],
  ]);
  const late = page.locator('#runupMarks .runup-mark.late');
  const today = page.locator('#runupMarks .runup-mark').nth(1);
  await expect(late).toHaveAttribute('aria-label', '1 overdue');
  await expect(today).toHaveAttribute('aria-label', '2 tasks, Jan 8');

  // A counted mark at today is drawn clear of the lamp, which lays it right
  // across the ring, and a ring nobody can press is a count of what is late
  // with no way to read it.
  expect(await middleOfTheRing(page)).toBe('1 overdue');
  await late.click();
  await expect(page.locator('.runup-pop li .what')).toHaveText(['Pay the florist']);
  await late.click();
  await expect(page.locator('.runup-pop')).toHaveCount(0);

  // And today's mark still opens its own list, from the part of it that
  // reaches past the ring: its middle is inside the ring, so that is where
  // this clicks rather than the middle Playwright would pick.
  await today.click({ position: { x: 30, y: 8 } });
  await expect(page.locator('.runup-pop li .what')).toHaveText(['Confirm the band', 'Collect the suits']);
});

test('the mark counting what is due on the day keeps its number in front of the day marker', async ({ page }) => {
  // Nine days to go, one thing behind, and the rest of the list on the day
  // itself: the day-of checklist. Anything due after the day is drawn on the
  // same spot, so this is the shape a plan takes at the end of it.
  await openWith(page, '2030-06-03T03:00:00Z', [
    ['Pay the florist', '2030-05-30', 'Grace'],
    ['Collect the suits', '2030-06-12', 'Ada'],
    ['Hand over the rings', '2030-06-12', 'Grace'],
  ]);
  const drawn = page.locator('#runupMarks .runup-mark');
  await expect(drawn).toHaveCount(2);
  await expect(drawn.nth(0)).toHaveAttribute('aria-label', '1 overdue');
  await expect(drawn.nth(1)).toHaveAttribute('aria-label', '2 tasks, Jun 12');
  expect(await drawn.nth(1).evaluate((el) => parseFloat(el.style.left))).toBe(100);

  // The day marker is filled and stands on that same spot. A counted mark sent
  // behind the marks goes behind it too, and the number is all such a mark
  // says.
  expect(await overTheCount(page)).toBe('2 tasks, Jun 12');
});

test('the word under the left end is not read out in front of the date', async ({ page }) => {
  await openWith(page, CROWDED_NOW, [
    ['Pay the florist', '2029-12-20', 'Grace'],
    ['Order the cake', '2030-03-01', 'Grace'],
  ]);
  // Both ends of the scale sit in one paragraph, so a reader that has no
  // scale to look at gets the two labels one after the other - "today" and
  // then the day, which together say the day is today.
  const ends = await page.locator('.runup-ends').ariaSnapshot();
  expect(ends).not.toMatch(/today|overdue/);
  expect(ends).toContain('June 12, 2030');

  // Both are still on the screen, and what is late is still announced, by the
  // mark that carries it rather than by the end of the line.
  await expect(page.locator('#runupFrom')).toBeVisible();
  await expect(page.locator('#runupFrom')).toHaveText('today – 1 overdue');
  await expect(page.locator('#runupMarks .runup-mark.late')).toHaveAttribute('aria-label', '1 overdue');
});

test('the months are ticked where they fall and named where there is room', async ({ page }) => {
  await openWith(page, CROWDED_NOW, CROWDED);
  const ticks = await page.locator('#runupTicks .runup-tick').evaluateAll((els) => els.map((el) => parseFloat(el.style.left)));
  // 1 February to 1 June, out of 155 days from 8 January: 24, 52, 83, 113, 144.
  expect(ticks.map((at) => Math.round(at * 155 / 100))).toEqual([24, 52, 83, 113, 144]);
  await expect(page.locator('#runupMonths .runup-month')).toHaveText(['Feb', 'Mar', 'Apr', 'May']);

  // On a phone the same ticks, and no month named on top of either end. Loaded
  // at that width rather than resized to it, so there is no redraw to wait on.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(page.locator('#runupTicks .runup-tick')).toHaveCount(5);
  await expect(page.locator('#runupMonths .runup-month').first()).toHaveText('Feb');
  const taken = await page.evaluate(() => ['runupFrom', 'statDaysLabel'].map((id) => {
    const r = document.getElementById(id).getBoundingClientRect();
    return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
  }));
  const months = await page.locator('#runupMonths .runup-month').evaluateAll((els) => els.map((el) => {
    const r = el.getBoundingClientRect();
    return { left: r.left, right: r.right, top: r.top, bottom: r.bottom };
  }));
  for (const m of months) for (const end of taken) expect(overlap(m, end)).toBe(false);
});

test('the money bar draws paid and owed against the ceiling', async ({ page }) => {
  await page.goto('/');
  await gotoTab(page, 'budget');
  await page.fill('#ceilingInput', '10000');
  await addBudgetLine(page, { item: 'Venue', unit: 2500, qty: 1, paid: 500 });
  await gotoTab(page, 'overview');

  const width = (id) => page.locator(id).evaluate((el) => parseFloat(el.style.width));
  expect(await width('#moneyBarPaid')).toBeCloseTo(5, 1);      // 500 of 10,000
  expect(await width('#moneyBarOwed')).toBeCloseTo(20, 1);     // 2,000 of 10,000
  const ceiling = page.locator('#moneyBarCeiling');
  await expect(ceiling).toBeVisible();
  expect(await ceiling.evaluate((el) => parseFloat(el.style.left))).toBeCloseTo(100, 1);
  await expect(ceiling).not.toHaveClass(/over/);

  // Past the ceiling the bar is the commitment, and the ceiling a mark inside it.
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Catering', unit: 17500, qty: 1, paid: 0 });
  await gotoTab(page, 'overview');
  expect(await width('#moneyBarPaid')).toBeCloseTo(2.5, 1);    // 500 of 20,000
  expect(await width('#moneyBarOwed')).toBeCloseTo(97.5, 1);
  expect(await ceiling.evaluate((el) => parseFloat(el.style.left))).toBeCloseTo(50, 1);
  await expect(ceiling).toHaveClass(/over/);

  // No ceiling, no mark.
  await gotoTab(page, 'budget');
  await page.fill('#ceilingInput', '0');
  await gotoTab(page, 'overview');
  await expect(ceiling).toBeHidden();
});
