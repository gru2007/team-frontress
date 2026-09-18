import React, { useEffect, useEffectEvent, useRef, useState } from "react";
import { Button, Group, GroupItem } from "@telegram-tools/ui-kit";

export default function Admin({ api, confirm, onSelfChange, userID }) {
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [busy, setBusy] = useState("");
  const [keys, setKeys] = useState("");
  const [text, setText] = useState("");
  const [feedback, setFeedback] = useState({});
  const lock = useRef(false);
  const request = useRef(0);
  const keysForm = useRef(null);
  const announcementForm = useRef(null);

  async function loadAdmin() {
    const version = ++request.current;
    setLoading(true);
    setLoadError("");
    try {
      const result = await api("/api/admin");
      if (version === request.current) setData(result);
    } catch (error) {
      if (version === request.current) {
        setLoadError(`Не удалось обновить данные: ${error.message}`);
      }
    } finally {
      if (version === request.current) setLoading(false);
    }
  }

  const initialLoad = useEffectEvent(loadAdmin);
  useEffect(() => {
    initialLoad();
    return () => { request.current += 1; };
  }, []);

  async function submit(event, kind) {
    event.preventDefault();
    if (lock.current) return;
    const value = (kind === "keys" ? keys : text).trim();
    if (!value) {
      setFeedback((previous) => ({
        ...previous,
        [kind]: { error: true, text: kind === "keys" ? "Добавьте хотя бы один ключ." : "Введите текст объявления." },
      }));
      return;
    }
    lock.current = true;
    setBusy(kind);
    setFeedback((previous) => ({ ...previous, [kind]: null }));
    try {
      if (kind === "publish" && !await confirm(
        "Опубликовать текст из формы? Объявление появится в ленте и будет отправлено участникам через бот.",
      )) return;
      const result = await api(
        kind === "keys" ? "/api/admin/keys" : "/api/admin/announcements",
        kind === "keys" ? { keys: value } : { text: value },
      );
      if (kind === "keys") setKeys("");
      else setText("");
      setFeedback((previous) => ({
        ...previous,
        [kind]: { text: kind === "keys"
          ? `Импортировано ключей: ${result.imported}. Повторы пропущены.`
          : "Объявление опубликовано. Рассылка поставлена в очередь." },
      }));
      await loadAdmin();
    } catch (error) {
      setFeedback((previous) => ({ ...previous, [kind]: { error: true, text: error.message } }));
    } finally {
      lock.current = false;
      setBusy("");
    }
  }

  async function changeAccess(person) {
    if (lock.current) return;
    const id = Number(person.telegram_id);
    if (!Number.isSafeInteger(id) || id <= 0) return;
    const restore = Boolean(person.key_revoked);
    const kind = `participant-${id}`;
    lock.current = true;
    setBusy(kind);
    setFeedback((previous) => ({ ...previous, [kind]: null }));
    let attempted = false;
    let changed = false;
    try {
      if (!restore && !await confirm(`Отозвать ключ у Telegram ID ${id} и удалить участника из группы?`)) return;
      attempted = true;
      const result = await api(restore ? "/api/admin/restore-key" : "/api/admin/revoke-key", { telegram_id: id });
      changed = true;
      setFeedback((previous) => ({
        ...previous,
        [kind]: { text: restore
          ? "Ключ восстановлен. Пользователь сможет снова подать заявку в группу."
          : result.removed_from_group
            ? "Ключ отозван. Участник удалён из группы."
            : "Ключ отозван; удаление из группы будет повторено автоматически." },
      }));
    } catch (error) {
      setFeedback((previous) => ({ ...previous, [kind]: { error: true, text: error.message } }));
    } finally {
      // Reconcile even failed requests: revocation may already have been persisted.
      if (attempted) await loadAdmin();
      try {
        if (changed && Number(userID) === id) await onSelfChange();
      } catch (error) {
        setFeedback((previous) => ({
          ...previous,
          [kind]: { error: true, text: `Доступ изменён, но не удалось обновить текущую сессию: ${error.message}` },
        }));
      } finally {
        lock.current = false;
        setBusy("");
      }
    }
  }

  function notice(kind) {
    const message = feedback[kind];
    return message ? (
      <p className={message.error ? "notice error" : "notice"} role={message.error ? "alert" : "status"}>
        {message.text}
      </p>
    ) : null;
  }

  return (
    <div className="page-stack">
      <section aria-label="Статистика" aria-busy={loading}>
        <Group header="Администрирование" action={
          <Button type="secondary" disabled={loading || Boolean(busy)} onClick={loadAdmin}>
            {loading ? "Загружаем…" : "Обновить данные"}
          </Button>
        }>
          <GroupItem main={
            <div className="stats-grid">
              {Object.entries({ participants: "Участников", linked: "Со Steam", available_keys: "Свободных ключей", issued_keys: "Активных ключей", revoked_keys: "Отозвано ключей", referrals: "Реферальных регистраций" }).map(([key, label]) => (
                <div className="stat" key={key}>
                  <strong>{data ? data.stats?.[key] ?? 0 : "—"}</strong>
                  <span>{label}</span>
                </div>
              ))}
            </div>
          } />
        </Group>
        {loadError && <p className="notice error" role="alert">{loadError}</p>}
      </section>

      <form className="form-panel" ref={keysForm} onSubmit={(event) => submit(event, "keys")} aria-busy={busy === "keys"}>
        <label className="field">
          <span>Импорт ключей</span>
          <span className="muted">Один ключ на строку. Повторы пропускаются.</span>
          <textarea rows={5} required spellCheck={false} autoComplete="off" value={keys}
            disabled={Boolean(busy)} onChange={(event) => setKeys(event.target.value)}
            placeholder="Вставьте ключи, каждый с новой строки" />
        </label>
        <Button type="secondary" disabled={Boolean(busy)} onClick={() => keysForm.current.requestSubmit()}>
          {busy === "keys" ? "Импортируем…" : "Импортировать ключи"}
        </Button>
        {notice("keys")}
      </form>

      <form className="form-panel" ref={announcementForm} onSubmit={(event) => submit(event, "publish")} aria-busy={busy === "publish"}>
        <label className="field">
          <span>Новое объявление</span>
          <span className="muted">До 2000 символов. Публикация в ленте и рассылка через бот.</span>
          <textarea rows={5} required maxLength={2000} value={text}
            disabled={Boolean(busy)} onChange={(event) => setText(event.target.value)}
            placeholder="Что важно знать участникам?" />
        </label>
        <Button disabled={Boolean(busy)} onClick={() => announcementForm.current.requestSubmit()}>
          {busy === "publish" ? "Публикуем…" : "Опубликовать…"}
        </Button>
        {notice("publish")}
      </form>

      <section aria-label="Участники" aria-busy={loading}>
        <p className="section-caption">Участники</p>
        <Group>
          {(data?.participants || []).map((person) => {
            const kind = `participant-${person.telegram_id}`;
            return (
              <GroupItem key={person.telegram_id} main={
                <div className="participant" aria-busy={busy === kind}>
                  <strong>{person.first_name || "Без имени"}</strong>
                   <div className="muted">Telegram ID: {person.telegram_id}</div>
                   <div className="muted">Пригласил: {person.referrer_id || "—"} · Приглашено: {person.referrals || 0}</div>
                  <div>Steam: {person.steam_id || "Не привязан"}</div>
                  <div>Ключ: {person.key_revoked ? "Отозван" : person.has_key ? "Выдан" : "Не выдан"}</div>
                  {(person.has_key || person.key_revoked) && (
                    <Button type="secondary" disabled={Boolean(busy) || loading} onClick={() => changeAccess(person)}>
                      {busy === kind ? "Сохраняем…" : person.key_revoked ? "Восстановить" : "Отозвать"}
                    </Button>
                  )}
                  {notice(kind)}
                </div>
              } />
            );
          })}
          {!data?.participants?.length && (
            <GroupItem text={loading ? "Загружаем участников…" : data ? "Участников пока нет." : "Данные участников недоступны."} />
          )}
        </Group>
      </section>
    </div>
  );
}
