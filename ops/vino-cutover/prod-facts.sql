select 'products:' x;
select pm.meta_value ae, p.ID, p.post_status, left(p.post_title,45) t, ifnull((select meta_value from x4gd_postmeta h where h.post_id=p.ID and h.meta_key='vino_hide_seat_number'),'-') hide from x4gd_posts p join x4gd_postmeta pm on pm.post_id=p.ID and pm.meta_key='bil24_action_event_id' where p.post_type='product' and p.post_status='publish' order by pm.meta_value;
select 'mailin active:', option_value like '%mailin%' from x4gd_options where option_name='active_plugins';
select 'orders last 24h:', count(*) from x4gd_wc_orders where date_created_gmt > utc_timestamp() - interval 1 day and type='shop_order';
select 'wpml product sync:', substring(option_value, locate('s:7:"product";', option_value), 22) from x4gd_options where option_name='icl_sitepress_settings';
