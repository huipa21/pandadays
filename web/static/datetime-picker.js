(() => {
  "use strict";

  const pad = value => String(value).padStart(2, "0");
  const dateText = date => `${String(date.getUTCFullYear()).padStart(4, "0")}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}`;
  function civilDate(year, month, day) {
    const date = new Date(0);
    date.setUTCFullYear(year, month - 1, day);
    return date;
  }
  const number = value => /^\d+$/.test(value) ? Number(value) : NaN;
  const dayMillis = 86400000;

  function init() {
    document.querySelectorAll("[data-datetime-picker]").forEach(root => {
      if (root.dataset.ready) return;
      root.dataset.ready = "true";
      const solar = root.querySelector('[name="date"]');
      const time = root.querySelector('[name="time"]');
      const lunarFields = ["year", "month", "day"].map(key => root.querySelector(`[name="lunar_${key}"]`));
      const toggle = root.querySelector("[data-calendar-toggle]");
      const digits = Array.from(root.dataset.lunarDigits);
      const monthNames = root.dataset.lunarMonths.split(",");
      const dayNames = root.dataset.lunarDays.split(",");
      const months = [];
      for (const year of JSON.parse(root.dataset.lunarData)) {
        let start = Date.parse(`${year.start}T00:00:00Z`);
        let month = 0;
        for (const length of year.months) {
          if (length > 0) month++;
          months.push({ year: year.year, month: length < 0 ? -month : month, days: Math.abs(length), start });
          start += Math.abs(length) * dayMillis;
        }
      }
      const weekdayFormat = new Intl.DateTimeFormat(document.documentElement.lang, { weekday: "short", timeZone: "UTC" });
      let lunar = toggle.checked;
      const group = document.createElement("div");
      group.className = "datetime-segments";
      group.setAttribute("role", "group");
      group.setAttribute("aria-label", root.closest("fieldset").querySelector("legend").textContent);
      const toggleLabel = toggle.closest("label");
      if (toggleLabel) {
        toggleLabel.classList.add("datetime-calendar-toggle");
        group.append(toggleLabel);
      }
      const editors = ["year", "month", "day", "hour", "minute"].map((key, index) => {
        if (key === "minute") {
          const colon = document.createElement("span");
          colon.textContent = ":";
          colon.setAttribute("aria-hidden", "true");
          group.append(colon);
        }
        const input = document.createElement("input");
        input.type = "text";
        input.inputMode = "numeric";
        input.autocomplete = "off";
        input.placeholder = ["YYYY", "MM", "DD", "HH", "MM"][index];
        input.setAttribute("aria-label", root.dataset[key]);
        input.setAttribute("aria-describedby", "day-time-hint");
        input.dataset.segment = key;
        input.required = index < 3;
        input.addEventListener("input", () => {
          input.dataset.lunarDraft = "true";
          if (lunar && index === 1 && input.value.trim().startsWith(root.dataset.lunarLeap)) leap.checked = true;
          validate(index < 3);
        });
        input.addEventListener("change", () => {
          if (validate(index < 3)) {
            input.dataset.lunarDraft = "false";
            format(index);
          }
        });
        input.addEventListener("focus", () => input.select());
        group.append(input);
        return input;
      });
      const weekday = document.createElement("output");
      weekday.className = "datetime-weekday";
      weekday.setAttribute("aria-live", "polite");
      group.append(weekday);
      const leapLabel = document.createElement("label");
      leapLabel.className = "day-calendar-check datetime-leap";
      const leap = document.createElement("input");
      leap.type = "checkbox";
      leap.setAttribute("aria-label", root.dataset.lunarLeap);
      const leapText = document.createElement("span");
      leapText.textContent = root.dataset.lunarLeap;
      leapLabel.append(leap, leapText);
      leap.addEventListener("change", () => {
        leap.dataset.lunarDraft = "true";
        if (validate(true)) format(1);
      });
      const clear = document.createElement("button");
      clear.type = "button";
      clear.className = "link-button datetime-clear";
      clear.textContent = root.dataset.clear;
      clear.addEventListener("click", () => {
        editors.slice(3).forEach(input => {
          input.value = "";
          input.dataset.lunarDraft = "false";
        });
        validate(false);
      });
      const status = document.createElement("p");
      status.className = "hint";
      status.setAttribute("role", "status");
      status.hidden = true;
      root.append(group, leapLabel, clear, status);
      root.classList.add("datetime-enhanced");

      function notify(field) {
        field.dispatchEvent(new Event("input", { bubbles: true }));
        field.dispatchEvent(new Event("change", { bubbles: true }));
      }
      function parse(index) {
        let value = editors[index].value.trim();
        if (!lunar || index > 2) return number(value);
        if (index === 0) {
          value = value.replace(new RegExp(`${root.dataset.lunarYear}$`), "");
          return number(Array.from(value, digit => {
            const at = digits.indexOf(digit);
            return at < 0 ? digit : String(at);
          }).join(""));
        }
        if (index === 2) return dayNames.includes(value) ? dayNames.indexOf(value) + 1 : number(value);
        value = value.replace(new RegExp(`^${root.dataset.lunarLeap}`), "");
        return monthNames.includes(value) ? monthNames.indexOf(value) + 1 : number(value);
      }
      function format(index) {
        const value = parse(index);
        if (!Number.isInteger(value)) return;
        editors[index].value = lunar && index < 3
          ? index === 0 ? Array.from(String(value), digit => digits[Number(digit)]).join("") + root.dataset.lunarYear
            : index === 1 ? monthNames[value - 1] : dayNames[value - 1]
          : index === 0 ? String(value).padStart(4, "0") : pad(value);
      }
      function validate(dateChanged) {
        if (!dateChanged) {
          const [hour, minute] = editors.slice(3).map(input => input.value.trim());
          const valid = (!hour && !minute) || (/^\d{1,2}$/.test(hour) && /^\d{1,2}$/.test(minute) && Number(hour) < 24 && Number(minute) < 60);
          editors.slice(3).forEach(input => input.setCustomValidity(valid ? "" : root.dataset.timeError));
          if (valid) {
            time.value = hour ? `${pad(Number(hour))}:${pad(Number(minute))}` : "";
            notify(time);
          }
          return valid;
        }
        const [year, month, day] = [0, 1, 2].map(parse);
        let date;
        let message = "";
        if (lunar) {
          const entry = months.find(entry => entry.year === year && entry.month === (leap.checked ? -month : month));
          if (entry && day >= 1 && day <= entry.days && Number.isInteger(day)) date = new Date(entry.start + (day - 1) * dayMillis);
          else message = root.dataset.rangeError;
        } else {
          date = civilDate(year, month, day);
          if (year < 1 || year > 9999 || date.getUTCFullYear() !== year || date.getUTCMonth() + 1 !== month || date.getUTCDate() !== day) {
            date = null;
            message = root.dataset.invalid;
          }
        }
        editors.slice(0, 3).forEach(input => input.setCustomValidity(message));
        weekday.textContent = date ? weekdayFormat.format(date) : "";
        status.textContent = message;
        status.hidden = !message;
        if (!date) return false;
        solar.value = dateText(date);
        if (lunar) {
          lunarFields.forEach((field, index) => { field.value = String([year, leap.checked ? -month : month, day][index]); });
          leap.dataset.lunarDraft = "false";
          notify(lunarFields[2]);
        } else notify(solar);
        return true;
      }
      function show(dateOnly = false) {
        solar.disabled = lunar;
        solar.required = !lunar;
        lunarFields.forEach(field => { field.disabled = !lunar; field.required = lunar; });
        leapLabel.hidden = !lunar;
        leap.dataset.lunarDraft = "false";
        const values = lunar ? lunarFields.map(field => field.value ? Number(field.value) : "") : solar.value ? solar.value.split("-").map(Number) : ["", "", ""];
        leap.checked = lunar && values[1] < 0;
        if (lunar && values[1] !== "") values[1] = Math.abs(values[1]);
        const clock = time.value ? time.value.split(":") : ["", ""];
        [...values, ...clock].forEach((value, index) => {
          if (dateOnly && index > 2) return;
          editors[index].value = value === undefined || Number.isNaN(value) ? "" : String(value);
          editors[index].dataset.lunarDraft = "false";
          editors[index].setCustomValidity("");
          format(index);
        });
        status.hidden = true;
        weekday.textContent = solar.value ? weekdayFormat.format(new Date(`${solar.value}T00:00:00Z`)) : "";
      }
      toggle.addEventListener("change", () => {
        if (!validate(true)) {
          toggle.checked = lunar;
          editors[0].reportValidity();
          return;
        }
        if (toggle.checked) {
          const value = Date.parse(`${solar.value}T00:00:00Z`);
          const entry = months.find(entry => value >= entry.start && value < entry.start + entry.days * dayMillis);
          if (!entry) {
            toggle.checked = false;
            status.textContent = root.dataset.rangeError;
            status.hidden = false;
            return;
          }
          lunarFields.forEach((field, index) => { field.value = String([entry.year, entry.month, (value - entry.start) / dayMillis + 1][index]); });
        }
        lunar = toggle.checked;
        show(true);
      });
      [...lunarFields, solar, time].forEach((field, index) => field.addEventListener("invalid", event => {
        event.preventDefault();
        editors[index < 3 ? index : index === 3 ? 0 : 3].focus();
      }));
      show();
      if (editors.slice(0, 3).every(input => input.value)) validate(true);
    });
  }

  init();
  document.addEventListener("htmx:load", init);
})();
