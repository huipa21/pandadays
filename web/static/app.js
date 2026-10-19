// Shows feedback for htmx requests: server/network errors (htmx does not swap
// error responses, so they would otherwise fail silently) and upload progress.
(function () {
  "use strict";

  var themeToggle = document.getElementById("theme-toggle");
  function syncThemeToggle() {
    if (themeToggle) themeToggle.setAttribute("aria-pressed", String(document.documentElement.dataset.theme === "dark"));
  }
  syncThemeToggle();
  if (themeToggle) {
    themeToggle.addEventListener("click", function () {
      var theme = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
      document.documentElement.dataset.theme = theme;
      document.cookie = "panda_theme=" + theme + "; Path=/; Max-Age=31536000; SameSite=Lax" +
        (window.location.protocol === "https:" ? "; Secure" : "");
      syncThemeToggle();
    });
  }

  function flash() {
    return document.getElementById("flash");
  }

  function showError(message) {
    var el = flash();
    if (!el) return;
    el.textContent = message;
    el.hidden = false;
    el.scrollIntoView({ block: "nearest" });
  }

  function serverMessage(xhr) {
    var type = (xhr.getResponseHeader("Content-Type") || "").toLowerCase();
    var text = (xhr.responseText || "").trim();
    if (type.indexOf("text/plain") === 0 && text && text.length <= 500) return text;
    return flash() ? flash().dataset.errorGeneric : "";
  }

  function progressFor(elt) {
    return elt && elt.querySelector ? elt.querySelector("[data-upload-progress]") : null;
  }

  function changed(input) {
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
  }

  function initPickers(root) {
    root.querySelectorAll("[data-tag-picker]").forEach(function (picker) {
      if (picker.dataset.ready) return;
      picker.dataset.ready = "true";
      var fallback = picker.querySelector("[data-tag-fallback]");
      var editor = picker.querySelector("[data-tag-editor]");
      var queryLabel = picker.querySelector("[data-tag-query-label]");
      if (queryLabel) {
        queryLabel.querySelector("[data-tag-query-text]").classList.add("visually-hidden");
        editor.append(queryLabel);
      }
      var input = editor.querySelector("textarea");
      var chips = editor.querySelector("[data-tag-chips]");
      var list = editor.querySelector("[role=listbox]");
      var empty = editor.querySelector("[data-tag-empty]");
      var newTags = picker.querySelector("[data-new-tags]");
      var tags = Array.from(fallback.querySelectorAll("label")).map(function (label) {
        return {
          input: label.querySelector("input"),
          name: label.querySelector("[data-tag-name]").textContent.trim(),
          swatch: label.querySelector("svg")
        };
      });
      if (newTags) {
        newTags.querySelectorAll("input").forEach(function (hidden) {
          tags.push({ input: hidden, name: hidden.value, swatch: null });
          hidden.checked = true;
        });
      }
      var matches = [];
      var active = 0;
      var query = null;

      function closeSuggestions() {
        list.hidden = true;
        empty.hidden = true;
        input.setAttribute("aria-expanded", "false");
        input.removeAttribute("aria-activedescendant");
      }
      function drawChips() {
        chips.replaceChildren();
        tags.filter(function (tag) { return tag.input.checked; }).forEach(function (tag) {
          var chip = document.createElement("button");
          chip.type = "button";
          chip.className = "chip tag-chip";
          chip.setAttribute("aria-label", picker.dataset.removeLabel + " " + tag.name);
          if (tag.swatch) chip.append(tag.swatch.cloneNode(true));
          chip.append(document.createTextNode(tag.name));
          var cross = document.createElement("span");
          cross.textContent = "\u00d7";
          cross.setAttribute("aria-hidden", "true");
          chip.append(cross);
          chip.addEventListener("click", function () {
            tag.input.checked = false;
            if (tag.input.name === "new_tags") tag.input.disabled = true;
            changed(tag.input);
            input.focus();
          });
          chips.append(chip);
        });
      }
      function highlight() {
        Array.from(list.children).forEach(function (option, index) {
          option.setAttribute("aria-selected", String(index === active));
        });
        var option = list.children[active];
        if (option) {
          input.setAttribute("aria-activedescendant", option.id);
          option.scrollIntoView({ block: "nearest" });
        }
      }
      function select(tag) {
        if (!tag.input) {
          var hidden = document.createElement("input");
          hidden.type = "hidden";
          hidden.name = "new_tags";
          hidden.value = tag.name;
          newTags.append(hidden);
          tag.input = hidden;
          tag.swatch = null;
          tags.push(tag);
        }
        tag.input.checked = true;
        tag.input.disabled = false;
        input.value = input.value.slice(0, query.start) + input.value.slice(query.end);
        var caret = query.start;
        changed(tag.input);
        changed(input);
        closeSuggestions();
        input.focus();
        input.setSelectionRange(caret, caret);
      }
      function suggest() {
        var caret = input.selectionStart;
        var match = input.value.slice(0, caret).match(/(?:^|\s)#([^#\n]*)$/);
        closeSuggestions();
        if (!match) return;
        query = { start: caret - match[1].length - 1, end: caret };
        var term = match[1].trim().toLocaleLowerCase();
        matches = tags.filter(function (tag) {
          return !tag.input.checked && tag.name.toLocaleLowerCase().includes(term);
        });
        if (term && newTags && !tags.some(function (tag) {
          return tag.name.toLocaleLowerCase() === term;
        })) {
          matches.push({ name: match[1].trim(), create: true });
        }
        list.replaceChildren();
        active = 0;
        matches.forEach(function (tag) {
          var option = document.createElement("button");
          option.type = "button";
          option.id = "tag-option-" + (tag.input ? tag.input.value : "new");
          option.className = "tag-option";
          option.setAttribute("role", "option");
          if (tag.swatch) option.append(tag.swatch.cloneNode(true));
          option.append(document.createTextNode((tag.create ? picker.dataset.createLabel + " " : "") + tag.name));
          option.addEventListener("mousedown", function (event) { event.preventDefault(); });
          option.addEventListener("click", function () { select(tag); });
          list.append(option);
        });
        list.hidden = matches.length === 0;
        empty.hidden = matches.length !== 0;
        input.setAttribute("aria-expanded", String(matches.length > 0));
        highlight();
      }
      input.addEventListener("input", function (event) {
        if (!event.isComposing) suggest();
      });
      input.addEventListener("compositionend", suggest);
      input.addEventListener("click", suggest);
      input.addEventListener("keydown", function (event) {
        if (event.isComposing) return;
        if (event.key === "Escape") {
          closeSuggestions();
        } else if (!list.hidden && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
          event.preventDefault();
          active = (active + (event.key === "ArrowDown" ? 1 : matches.length - 1)) % matches.length;
          highlight();
        } else if (!list.hidden && event.key === "Enter") {
          event.preventDefault();
          select(matches[active]);
        }
      });
      picker.addEventListener("change", function (event) {
        if (event.target.name === "tags" || event.target.name === "new_tags") drawChips();
      });
      picker.addEventListener("focusout", function (event) {
        if (!picker.contains(event.relatedTarget)) closeSuggestions();
      });
      document.addEventListener("click", function (event) {
        if (!picker.contains(event.target)) closeSuggestions();
      });
      fallback.hidden = true;
      editor.hidden = false;
      picker.querySelector("[data-tag-help]").hidden = false;
      drawChips();
    });

    root.querySelectorAll("[data-icon-picker]").forEach(function (picker) {
      if (picker.dataset.ready) return;
      picker.dataset.ready = "true";
      var input = picker.querySelector("input");
      var options = picker.querySelector("[data-icon-options]");
      var clear = picker.querySelector("[data-icon-clear]");
      var icons = [
        "🎉", "🎂", "🎁", "🎈", "🎊", "🥳", "❤️", "💕", "💍", "💐", "🌹", "🥂",
        "👶", "🐼", "🐱", "🐶", "🐾", "🏠", "👨‍👩‍👧", "🤝", "😊", "🌟", "✨", "🍀",
        "🎓", "📚", "✏️", "💼", "💻", "📅", "📌", "🎯", "🏆", "🥇", "🚀", "💡",
        "✈️", "🚗", "🚆", "🚲", "🏖️", "🏕️", "🏔️", "🌍", "🗺️", "📷", "🧳", "⛵",
        "🎵", "🎸", "🎹", "🎨", "🎬", "🎮", "⚽", "🏀", "🎾", "🏊", "🏃", "🧘",
        "☀️", "🌙", "🌈", "🌸", "🌻", "🌱", "🍂", "❄️", "🎄", "🎃", "🔥", "💫",
        "☕", "🍰", "🍕", "🍎", "🍜", "🍽️", "💪", "🩺", "💰", "🛠️", "⏳", "✅"
      ];
      icons.forEach(function (icon) {
        var button = document.createElement("button");
        button.type = "button";
        button.className = "icon-option";
        button.textContent = icon;
        button.addEventListener("click", function () {
          input.value = icon;
          changed(input);
        });
        options.append(button);
      });
      function sync() {
        Array.from(options.children).forEach(function (button) {
          button.setAttribute("aria-pressed", String(button.textContent === input.value));
        });
        clear.disabled = input.value === "";
      }
      clear.addEventListener("click", function () {
        input.value = "";
        changed(input);
      });
      input.addEventListener("input", sync);
      options.hidden = false;
      clear.hidden = false;
      sync();
    });

    root.querySelectorAll("[data-color-picker]").forEach(function (picker) {
      if (picker.dataset.ready) return;
      picker.dataset.ready = "true";
      var input = picker.querySelector('input[type="color"]');
      var none = picker.querySelector('input[name="no_color"]');
      var presets = picker.querySelector("[data-color-presets]");
      var custom = picker.querySelector("[data-custom-color]");
      var colors = [
        "#ef4444", "#f97316", "#f59e0b", "#eab308", "#84cc16", "#22c55e",
        "#10b981", "#14b8a6", "#06b6d4", "#0ea5e9", "#3b82f6", "#6366f1",
        "#8b5cf6", "#a855f7", "#d946ef", "#ec4899", "#78716c", "#64748b",
        "#1e293b", "#ffffff"
      ];
      colors.forEach(function (color) {
        var button = document.createElement("button");
        button.type = "button";
        button.className = "color-option";
        button.dataset.color = color;
        button.setAttribute("aria-label", presets.parentNode.getAttribute("aria-label") + " " + color);
        button.title = color;
        var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
        svg.setAttribute("viewBox", "0 0 32 32");
        svg.setAttribute("aria-hidden", "true");
        var circle = document.createElementNS("http://www.w3.org/2000/svg", "circle");
        circle.setAttribute("cx", "16");
        circle.setAttribute("cy", "16");
        circle.setAttribute("r", "13");
        circle.setAttribute("fill", color);
        svg.append(circle);
        button.append(svg);
        button.addEventListener("click", function () {
          input.value = color;
          none.checked = false;
          changed(input);
        });
        presets.append(button);
      });
      function sync() {
        var selected = input.value.toLowerCase();
        Array.from(presets.children).forEach(function (button) {
          button.setAttribute("aria-pressed", String(!none.checked && button.dataset.color === selected));
        });
        custom.classList.toggle("is-selected", !none.checked && !colors.includes(selected));
      }
      input.addEventListener("input", function () {
        none.checked = false;
        sync();
      });
      none.addEventListener("change", sync);
      picker.classList.add("color-picker-enhanced");
      picker.querySelector("[data-color-label]").classList.add("visually-hidden");
      presets.hidden = false;
      sync();
    });
  }
  initPickers(document);

  document.querySelectorAll("dialog[data-auto-open]").forEach(function (dialog) {
    dialog.close();
    dialog.showModal();
  });

  var dayForm = document.querySelector("[data-day-form]");
  if (dayForm) {
    function dayFormValues() {
      return JSON.stringify(Array.from(new FormData(dayForm).entries()).filter(function (entry) {
        return entry[0] !== "csrf_token";
      }));
    }
    var initialDayValues = dayFormValues();
    var daySubmission = null;
    window.addEventListener("beforeunload", function (event) {
      // A valid save may leave the page; canceled submissions must remain protected.
      if (daySubmission && !daySubmission.defaultPrevented) return;
      if (dayForm.dataset.unsaved !== "true" && dayFormValues() === initialDayValues) return;
      event.preventDefault();
      event.returnValue = "";
    });
    dayForm.addEventListener("submit", function (event) {
      daySubmission = event;
    });
    dayForm.addEventListener("input", function () {
      daySubmission = null;
    });
    dayForm.addEventListener("change", function () {
      daySubmission = null;
    });
    window.addEventListener("pageshow", function () {
      daySubmission = null;
    });
  }

  document.addEventListener("submit", function (event) {
    var message = event.target.dataset.confirm;
    if (message && !window.confirm(message)) event.preventDefault();
  });

  document.addEventListener("click", function (event) {
    var opener = event.target.closest ? event.target.closest("[data-open-dialog]") : null;
    if (opener) {
      var dialog = document.getElementById(opener.dataset.openDialog);
      if (dialog && dialog.showModal) {
        event.preventDefault();
        dialog.showModal();
      }
    }
    var cancel = event.target.closest ? event.target.closest("dialog [data-close-dialog]") : null;
    if (!cancel) return;
    event.preventDefault();
    cancel.closest("dialog").close();
  });

  document.addEventListener("htmx:beforeRequest", function (event) {
    var menu = event.target.closest ? event.target.closest(".dropdown[open]") : null;
    if (menu) menu.open = false;
  });

  document.addEventListener("htmx:afterSwap", function (event) {
    var dialog = event.detail.target;
    if (!dialog || dialog.id !== "app-dialog") return;
    initPickers(dialog);
    localizeTimes(dialog);
    if (dialog.showModal && !dialog.open) dialog.showModal();
    dialog.scrollTop = 0;
  });

  // Users without a saved timezone see server times re-rendered in the browser's timezone.
  function pad(n) {
    return (n < 10 ? "0" : "") + n;
  }
  function localizeTimes(root) {
    root.querySelectorAll("time[data-local-time]").forEach(function (el) {
      var d = new Date(el.getAttribute("datetime"));
      if (isNaN(d.getTime())) return;
      var zone = "";
      try {
        var part = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(d)
          .find(function (p) { return p.type === "timeZoneName"; });
        if (part) zone = " " + part.value;
      } catch (e) {}
      el.textContent = d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " +
        pad(d.getHours()) + ":" + pad(d.getMinutes()) + zone;
    });
  }
  localizeTimes(document);

  document.addEventListener("click", function (event) {
    document.querySelectorAll(".dropdown[open]").forEach(function (menu) {
      if (!menu.contains(event.target)) menu.open = false;
    });
  });

  document.addEventListener("keydown", function (event) {
    if (event.key !== "Escape") return;
    document.querySelectorAll(".dropdown[open]").forEach(function (menu) {
      menu.open = false;
      menu.querySelector("summary").focus();
    });
  });

  document.addEventListener("htmx:beforeRequest", function (event) {
    var el = flash();
    if (el) el.hidden = true;
    var bar = progressFor(event.target);
    if (bar) bar.hidden = false;
  });

  document.addEventListener("htmx:responseError", function (event) {
    showError(serverMessage(event.detail.xhr));
  });

  document.addEventListener("htmx:sendError", function () {
    var el = flash();
    if (el) showError(el.dataset.errorNetwork);
  });

  document.addEventListener("htmx:xhr:progress", function (event) {
    var bar = progressFor(event.target);
    if (!bar || !event.detail.lengthComputable || !event.detail.total) return;
    bar.hidden = false;
    bar.value = Math.round((event.detail.loaded / event.detail.total) * 100);
  });

  document.addEventListener("htmx:afterRequest", function (event) {
    var bar = progressFor(event.target);
    if (!bar) return;
    bar.hidden = true;
    bar.removeAttribute("value");
  });
})();
